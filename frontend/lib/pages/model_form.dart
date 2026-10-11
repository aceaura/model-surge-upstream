import 'package:flutter/material.dart';

import '../api_client.dart';
import '../models.dart';
import '../ui/collapsible_section.dart';
import '../ui/feedback.dart';
import '../ui/form_page.dart';
import '../ui/json_field.dart';
import '../ui/provider_avatar.dart';
import '../ui/styled_dropdown.dart';
import '../ui/upstream_model_picker.dart';

/// 模型创建与编辑整页表单(CC Switch 式:页内内联替换列表,不推根路由,
/// 侧边栏保持可见;不用居中弹窗)。
/// 协议候选来自所选账号对应 provider 的支持集，随账号切换联动，
/// 避免提交注定被服务端拒绝的组合。
/// copyFrom 非空时为"拷贝创建":以该模型配置预填(标识加 -copy 后缀),
/// 仍是新建语义。
class ModelForm extends StatefulWidget {
  const ModelForm({
    super.key,
    required this.client,
    required this.accounts,
    required this.providers,
    required this.initialAccount,
    required this.onDone,
    this.editing,
    this.copyFrom,
  });

  final ApiClient client;
  final List<Account> accounts;
  final List<ProviderSpec> providers;
  final String initialAccount;

  /// 表单收尾回调:true=已保存(宿主需重载列表),false=放弃修改。
  final ValueChanged<bool> onDone;
  final UpstreamModel? editing;

  /// 拷贝来源:以其配置预填新建表单。
  final UpstreamModel? copyFrom;

  @override
  State<ModelForm> createState() => _ModelFormState();
}

class _ModelFormState extends State<ModelForm> {
  final _formKey = GlobalKey<FormState>();

  UpstreamModel? get _source => widget.editing ?? widget.copyFrom;

  late final TextEditingController _id = TextEditingController(
    text:
        widget.editing?.id ??
        (widget.copyFrom != null ? '${widget.copyFrom!.id}-copy' : ''),
  );
  late final TextEditingController _nativeModel = TextEditingController(
    text: _source?.nativeModel ?? '',
  );
  // 上下文窗口按 k 单位录入/回显(1k = 1000 tokens),提交时换回 token 数
  late final TextEditingController _contextWindow = TextEditingController(
    text: tokensToK(_source?.contextWindow ?? 0),
  );
  late final TextEditingController _defaults = TextEditingController(
    text: prettyJson(_source?.defaults ?? const {}),
  );
  late final TextEditingController _overrides = TextEditingController(
    text: prettyJson(_source?.overrides ?? const {}),
  );

  // 上下文限制开关:开=error(估算超窗即拦截,回 400 让客户端自压缩)、
  // 关=passive(不拦截);UI 不再暴露 passive/error 词表。阈值按百分比录入
  // (85 = 窗口的 85%),提交时换回比例。存量 auto(网关代压,已废)载入归一为开。
  late String _compactMode = _normalizeCompactMode(_source?.compact['mode']);
  late final TextEditingController _compactThreshold = TextEditingController(
    text: _compactThresholdText(_source?.compact),
  );

  static String _normalizeCompactMode(Object? mode) {
    // 存量 auto(网关代压,已废)载入归一为 error;新建/未知值落 passive。
    if (mode == 'error' || mode == 'auto') return 'error';
    return 'passive';
  }

  static String _compactThresholdText(Map<String, dynamic>? compact) {
    final t = compact?['threshold'];
    if (t is num) return '${(t * 100).round()}';
    return '85';
  }

  late String _account = _source?.account ?? widget.initialAccount;
  String? _protocol;
  // 启停由列表行开关控制,表单不再展示;编辑/拷贝时沿用原值提交,新建默认启用
  late final bool _enabled = _source?.enabled ?? true;

  // 推理档=行式映射对(2026-10-11 重设计):每行一对「下游档 → 上行值」,
  // 下游档/上行值各按本端格式词表下拉(上游 budget 类右列为 token 数字或
  // none 的编辑框,下游 budget 类左列为阈值数字编辑框);none 进两端词表
  // 表示无思考,写上游按格式的关思考形态适配。没有自动跟随上游的模式;
  // 编辑/拷贝存量自动档(null)的模型时用当前有效列表预填,保存即落为显式
  // 声明;存量「行号即档号」的纯数字名读时迁移为映射对。
  late final List<_EffortRow> _effortRows = _initialEffort();
  // effort 写入格式:恒为显式格式,双端词表全量罗列(跨族组合合法——
  // 承接「协议外壳+自家字段」的厂商差异)。缺省与回落都取词表首项;
  // 存量旧值(空/四协议名)读时归一,不在词表落首项,下次保存即迁移。
  late String _effortFormat;
  // 下游格式(effort_in):同样全量词表恒显示,缺省=词表首项。
  late String _effortIn;
  // 0 档在 anthropic 族上游的关思考落定:空=disabled / between_tools / omit。
  late String _effortOff = _source?.effortOff ?? '';
  // 推理档转换总开关:false=转发面不读不写不剥离,对话页选档不落笔;
  // 其余推理档字段常驻显示(值保留,重新开启即用)。
  late bool _effortEnabled = _source?.effortEnabled ?? true;

  List<_EffortRow> _initialEffort() {
    final source = _source;
    if (source == null) return <_EffortRow>[];
    final entries = source.efforts ?? source.effortsEffective;
    // 下游 budget 格式下数字名是预算阈值行,不做存量档号迁移(与后端
    // effort.Effective 的 budgetEntry 守卫同口径)。
    final budgetEntry = source.effortIn == 'anthropic_budget';
    final seen = <String>{};
    final rows = <_EffortRow>[];
    for (final e in entries) {
      var name = e.name.trim();
      final value = e.value.trim();
      if (value.isEmpty) continue;
      // 存量「行号即档号」迁移(与后端 effort.Effective 同口径):纯数字名
      // 值 none → none→none,其余恒等;名留空取 value;按名去重留首个。
      if (int.tryParse(name) != null && !budgetEntry) {
        name = value.toLowerCase() == 'none' ? 'none' : value;
      }
      if (name.isEmpty) name = value;
      if (!seen.add(name)) continue;
      rows.add(_EffortRow(name, value));
    }
    return rows;
  }

  bool _defaultsValid = true;
  bool _overridesValid = true;
  bool _busy = false;

  bool get _isEdit => widget.editing != null;

  @override
  void initState() {
    super.initState();
    _protocol = _source?.protocol ?? _protocols.firstOrNull;
    _effortFormat = _upstreamInit(_source?.effortFormat ?? '');
    _effortIn = _entryInit(_source?.effortIn ?? '');
  }

  @override
  void dispose() {
    _id.dispose();
    _nativeModel.dispose();
    _contextWindow.dispose();
    _defaults.dispose();
    _overrides.dispose();
    _compactThreshold.dispose();
    super.dispose();
  }

  /// 所选账号 → 其 provider → 支持协议。
  List<String> get _protocols {
    final account = widget.accounts
        .where((a) => a.name == _account)
        .firstOrNull;
    if (account == null) return const [];
    final spec = widget.providers
        .where((p) => p.id == account.providerId)
        .firstOrNull;
    return spec?.protocols ?? const [];
  }

  /// 卡顶居中头像取所选账号的提供商,随账号切换联动。
  String get _avatarProvider =>
      widget.accounts
          .where((a) => a.name == _account)
          .firstOrNull
          ?.providerId ??
      '?';

  void _onAccountChanged(String? name) {
    if (name == null) return;
    setState(() {
      _account = name;
      // 新账号可能不支持原协议，重置到其首个可用协议。
      if (!_protocols.contains(_protocol)) {
        _protocol = _protocols.firstOrNull;
      }
    });
  }

  /// 组装 compact JSON 提交:模式必带;阈值仅 error 下生效。
  /// 缺项回落服务端全局默认。
  Map<String, dynamic> _buildCompact() {
    final out = <String, dynamic>{'mode': _compactMode};
    if (_compactMode != 'passive') {
      final pct = double.tryParse(_compactThreshold.text.trim());
      if (pct != null) out['threshold'] = pct / 100;
    }
    return out;
  }

  Future<void> _submit() async {
    if (!(_formKey.currentState?.validate() ?? false)) return;
    final (defaults, defaultsErr) = parseJsonObject(_defaults.text);
    final (overrides, overridesErr) = parseJsonObject(_overrides.text);
    if (defaultsErr != null || overridesErr != null) return;

    setState(() => _busy = true);
    try {
      if (_isEdit) {
        await widget.client.updateModel(
          id: widget.editing!.id,
          // 标识可改:写错的模型名在编辑页直接改名,服务端改写主键。
          newId: _id.text.trim(),
          account: _account,
          nativeModel: _nativeModel.text.trim(),
          protocol: _protocol,
          contextWindow: kToTokens(_contextWindow.text),
          defaults: defaults!,
          overrides: overrides!,
          compact: _buildCompact(),
          efforts: _efforts,
          effortFormat: _effortFormat,
          effortIn: _effortIn,
          effortOff: _effortOff,
          effortBudgets: _effortBudgets,
          effortEnabled: _effortEnabled,
          enabled: _enabled,
        );
      } else {
        await widget.client.createModel(
          id: _id.text.trim(),
          account: _account,
          nativeModel: _nativeModel.text.trim(),
          protocol: _protocol!,
          contextWindow: kToTokens(_contextWindow.text),
          defaults: defaults!,
          overrides: overrides!,
          compact: _buildCompact(),
          efforts: _efforts,
          effortFormat: _effortFormat,
          effortIn: _effortIn,
          effortOff: _effortOff,
          effortBudgets: _effortBudgets,
          effortEnabled: _effortEnabled,
          enabled: _enabled,
        );
      }
      if (!mounted) return;
      widget.onDone(true);
    } catch (e) {
      if (!mounted) return;
      showError(context, e);
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  bool get _canSubmit => _defaultsValid && _overridesValid;

  @override
  Widget build(BuildContext context) {
    // 面包屑两级:模型(回列表) / 当前动作(末级带完整标识区分账号)。
    return FormPage(
      breadcrumbs: [
        CrumbLevel('模型', onTap: () => widget.onDone(false)),
        CrumbLevel(
          _isEdit
              ? '编辑 ${widget.editing!.id}'
              : widget.copyFrom != null
              ? '拷贝 ${widget.copyFrom!.id}'
              : '新建模型',
        ),
      ],
      avatar: ProviderAvatar(providerId: _avatarProvider, size: 56),
      onCancel: () => widget.onDone(false),
      onSubmit: _submit,
      submitEnabled: _canSubmit,
      submitLabel: _isEdit ? '保存' : '创建',
      busy: _busy,
      child: Form(
        key: _formKey,
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            _basicSection(),
            const SizedBox(height: 26),
            _contextSection(),
            const SizedBox(height: 26),
            _effortSection(),
            const SizedBox(height: 26),
            _paramsSection(),
          ],
        ),
      ),
    );
  }

  // ── 基本信息(账号/协议/模型标识/上游模型名)──

  String get _basicSubtitle {
    final parts = [
      _account,
      _protocol ?? '',
      _nativeModel.text.trim(),
    ].where((s) => s.isNotEmpty).toList();
    return parts.isEmpty ? '账号、协议与上游模型' : parts.join(' · ');
  }

  Widget _basicSection() {
    return CollapsibleSection(
      icon: Icons.smart_toy_outlined,
      title: '基本信息',
      subtitle: _basicSubtitle,
      // 主信息默认展开,收起时靠副标题辨认当前配置。
      initiallyExpanded: true,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          // 账号与协议联动,并排成组(CC Switch 式双列)
          FormRow2(
            LabeledField(
              key: const ValueKey('model-account-field'),
              label: '账号',
              child: StyledDropdownFormField(
                key: const ValueKey('model-account'),
                value: _account,
                decoration: const InputDecoration(border: OutlineInputBorder()),
                options: [for (final a in widget.accounts) a.name],
                onChanged: _onAccountChanged,
              ),
            ),
            LabeledField(
              key: const ValueKey('model-protocol-field'),
              label: '协议',
              child: StyledDropdownFormField(
                key: ValueKey('protocol-$_account'),
                value: _protocol,
                decoration: const InputDecoration(border: OutlineInputBorder()),
                options: _protocols,
                onChanged: (v) => setState(() => _protocol = v),
                validator: (v) => v == null ? '请选择协议' : null,
              ),
            ),
          ),
          // 模型标识编辑态也可改:写错的模型名直接改名保存(服务端改写主键,
          // 会话回显随迁,用量历史保留旧标识)。
          const SizedBox(height: 20),
          LabeledField(
            label: '模型标识',
            child: TextFormField(
              key: const ValueKey('model-id'),
              controller: _id,
              decoration: const InputDecoration(
                hintText: 'kimi-1/k2',
                border: OutlineInputBorder(),
              ),
              validator: (v) =>
                  (v == null || v.trim().isEmpty) ? '模型标识不能为空' : null,
            ),
          ),
          const SizedBox(height: 20),
          LabeledField(
            label: '上游模型名',
            child: UpstreamModelPicker(
              client: widget.client,
              account: _account,
              value: _nativeModel.text,
              enabled: !_busy,
              onSelected: (value) => setState(() {
                _nativeModel.value = TextEditingValue(
                  text: value,
                  selection: TextSelection.collapsed(offset: value.length),
                );
              }),
              child: TextFormField(
                key: const ValueKey('model-native'),
                controller: _nativeModel,
                decoration: const InputDecoration(
                  hintText: '输入模型名，或从上游列表中选择',
                  border: OutlineInputBorder(),
                ),
                onChanged: (_) => setState(() {}),
                validator: (v) =>
                    (v == null || v.trim().isEmpty) ? '上游模型名不能为空' : null,
              ),
            ),
          ),
        ],
      ),
    );
  }

  // ── 上下文限制(窗口/压缩策略/触发阈值)──

  String get _contextSubtitle {
    final window = _contextWindow.text.trim();
    final mode = _compactMode == 'error' ? '已开启' : '已关闭';
    final w = window.isEmpty || window == '0' ? '窗口未声明' : '窗口 ${window}k';
    return '$w · $mode';
  }

  Widget _contextSection() {
    return CollapsibleSection(
      icon: Icons.compress_outlined,
      title: '上下文限制',
      subtitle: _contextSubtitle,
      initiallyExpanded: true,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          // 开关独占第一排;上下文大小与触发阈值并排第二排常驻(阈值仅开启时生效,
          // 关闭时保存不落 threshold,但值保留在框里供开启即用)
          LabeledField(
            key: const ValueKey('model-compact-mode-field'),
            label: '开启上下文限制',
            hint: '估算输入超过窗口比例时拦截，回 400 让客户端自压缩',
            child: SizedBox(
              height: 40,
              child: Row(
                children: [
                  // Switch 收缩包裹并左移抵掉内置 4px 水平内边距,轨道左缘
                  // 才能对齐输入框列(与推理档开关同款处理)
                  Transform.translate(
                    offset: const Offset(-4, 0),
                    child: Switch(
                      key: const ValueKey('model-compact-switch'),
                      value: _compactMode == 'error',
                      materialTapTargetSize: MaterialTapTargetSize.shrinkWrap,
                      onChanged: (v) => setState(
                        () => _compactMode = v ? 'error' : 'passive',
                      ),
                    ),
                  ),
                  const SizedBox(width: 8),
                  Text(
                    _compactMode == 'error' ? '已开启' : '已关闭',
                    style: const TextStyle(fontSize: 12.5),
                  ),
                ],
              ),
            ),
          ),
          const SizedBox(height: 20),
          FormRow2(
            LabeledField(
              label: '上下文大小',
              child: TextFormField(
                key: const ValueKey('model-context'),
                controller: _contextWindow,
                keyboardType: const TextInputType.numberWithOptions(
                  decimal: true,
                ),
                decoration: const InputDecoration(
                  suffixText: 'k',
                  border: OutlineInputBorder(),
                ),
                validator: (v) {
                  final n = double.tryParse(v?.trim() ?? '');
                  if (n == null) return '请填写数字';
                  if (n < 0) return '不能为负数';
                  return null;
                },
              ),
            ),
            LabeledField(
              key: const ValueKey('model-compact-threshold-field'),
              label: '触发阈值',
              child: TextFormField(
                key: const ValueKey('model-compact-threshold'),
                controller: _compactThreshold,
                keyboardType: const TextInputType.numberWithOptions(
                  decimal: true,
                ),
                decoration: const InputDecoration(
                  suffixText: '%',
                  border: OutlineInputBorder(),
                ),
                validator: (v) {
                  final n = double.tryParse(v?.trim() ?? '');
                  if (n == null) return '请填写数字';
                  if (n <= 0 || n > 100) return '须在 1–100 之间';
                  return null;
                },
              ),
            ),
          ),
        ],
      ),
    );
  }

  // ── 推理档(行式映射对:下游档 → 上行值)──

  /// 收集提交条目:每行一对 {下游档, 上行值};任一侧留空的行视为未填丢弃。
  /// 无档位类上游(anthropic_off)无映射表,恒提交空声明;已填行保留在
  /// 表单态里,切回载档格式即恢复。
  List<EffortEntry> get _efforts {
    if (_offCategory) return const [];
    return [
      for (final r in _effortRows)
        if (r.name.trim().isNotEmpty && r.value.trim().isNotEmpty)
          EffortEntry(name: r.name.trim(), value: r.value.trim()),
    ];
  }

  String get _effortSubtitle {
    // 双端格式恒显式(无 auto 默认项),折叠标题常驻转换方向。
    if (_offCategory) return '无档位（思考恒关） · 下游 $_effortIn → 上游 $_effortFormat';
    final n = _efforts.length;
    final base = n == 0 ? '不支持' : '$n 行映射';
    return '$base · 下游 $_effortIn → 上游 $_effortFormat';
  }

  /// 双端格式词表(2026-10-11 用户定:上游下游都用全部协议格式,不做
  /// 协议同族过滤——跨族组合承接「协议外壳+自家字段」的厂商差异)。
  /// 下游 6 种(词表不收 gemini 下游形态);上游 8 种(含 gemini 两族)。
  /// effort_index 已废:reasoning_level 数字档是网关扩展字段,不走显式格式。
  static const _entryFormats = [
    'openai_chat',
    'openai_responses',
    'anthropic_effort',
    'anthropic_budget',
    'anthropic_adaptive',
    'anthropic_off',
  ];
  static const _upstreamFormats = [
    'openai_chat',
    'openai_responses',
    'anthropic_effort',
    'anthropic_budget',
    'anthropic_adaptive',
    'anthropic_off',
    'gemini_level',
    'gemini_budget',
  ];

  /// 存量旧值(四协议名)→ 双端词表别名,与后端 effort.NormalizeFormat 同表。
  static String _normalizeFormat(String f) => switch (f) {
    'chat_completions' => 'openai_chat',
    'responses' => 'openai_responses',
    'anthropic' => 'anthropic_effort',
    'gemini' => 'gemini_level',
    _ => f,
  };

  /// 入口值初始化:归一后不在词表(存量空值 auto/未知值)落词表首项。
  static String _entryInit(String f) {
    final n = _normalizeFormat(f);
    return _entryFormats.contains(n) ? n : _entryFormats.first;
  }

  /// 上游值初始化:同理,存量空值/未知值落词表首项。
  static String _upstreamInit(String f) {
    final n = _normalizeFormat(f);
    return _upstreamFormats.contains(n) ? n : _upstreamFormats.first;
  }

  /// 关思考落定选项与标签(0 档在 anthropic 族上游怎么写)。
  static const _effortOffs = ['', 'between_tools', 'omit'];
  static String _offLabel(String o) => switch (o) {
    'between_tools' => 'between_tools（Sonnet 5.5 顶替 disabled）',
    'omit' => '不写（上游思考恒开，0 档仅靠不声明承担）',
    _ => 'disabled（标准：thinking 改写为 disabled）',
  };

  /// 上游格式三分类(2026-10-11 映射重设计),与后端 effort.CategoryOf 同表:
  /// effort 类档位直写、budget 类预算映射、off 类无档位(模型思考恒关)。
  /// 分类驱动填写框显隐:off 类不显示档位表/0 档开关/关思考落定。
  static const _formatCategories = <String, String>{
    'openai_chat': 'effort',
    'openai_responses': 'effort',
    'anthropic_effort': 'effort',
    'anthropic_adaptive': 'effort',
    'gemini_level': 'effort',
    'anthropic_budget': 'budget',
    'gemini_budget': 'budget',
    'anthropic_off': 'off',
  };

  /// 显式选定的上游格式(归一后,恒在词表内)。配对专属字段(关思考落定/
  /// 预算列)只在选定对应格式时出现。
  String get _upstreamFormat => _normalizeFormat(_effortFormat);

  String get _upstreamCategory => _formatCategories[_upstreamFormat] ?? '';

  /// 下游格式三分类(与 _formatCategories 同表):budget 类左列为阈值数字
  /// 编辑框,off 类只读 none,其余按词表下拉。
  String get _entryCategory => _formatCategories[_normalizeFormat(_effortIn)] ?? '';

  /// 档词表(2026-10-11 行式映射对设计),与后端 effort.LevelsFor 同表:
  /// openai 两族 7 值;anthropic 两族 none+官方 5 档;gemini_level none+
  /// LOW/HIGH。budget/off 类不入表(右列/左列为编辑框或无档位)。
  static const _levelVocabs = <String, List<String>>{
    'openai_chat': ['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'],
    'openai_responses': ['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'],
    'anthropic_effort': ['none', 'low', 'medium', 'high', 'xhigh', 'max'],
    'anthropic_adaptive': ['none', 'low', 'medium', 'high', 'xhigh', 'max'],
    'gemini_level': ['none', 'LOW', 'HIGH'],
  };

  /// 格式 → 档词表;budget/off/auto 回空表。
  static List<String> _levelsFor(String format) =>
      _levelVocabs[_normalizeFormat(format)] ?? const [];

  /// 下拉选项:词表 + 存量词表外当前值(私有档/大小写差异不丢,保存时由
  /// 后端词表校验把关)。
  static List<String> _options(List<String> vocab, String current) {
    if (current.isNotEmpty && !vocab.contains(current)) {
      return [...vocab, current];
    }
    return vocab;
  }

  /// 无档位类上游(anthropic_off):模型思考恒关,无需档位表与关思考落定。
  bool get _offCategory => _upstreamCategory == 'off';

  /// 关思考落定:仅 anthropic 族载档上游格式出现(其余格式忽略 effort_off;
  /// anthropic_off 本身即关思考,无需填)。
  bool get _offRelevant => const {
    'anthropic_effort',
    'anthropic_budget',
    'anthropic_adaptive',
  }.contains(_upstreamFormat);

  /// 预算覆盖表:映射对重设计后预算数直接填在上行值里(budget 类右列
  /// 编辑框),不再从档位行收集;存量覆盖原样提交保留。
  Map<String, int> get _effortBudgets => _source?.effortBudgets ?? const {};

  /// 格式切换后把已填行矫正进新词表(设计稿同款):进词表的保留,词表外
  /// 落第一个非 none 档;budget 类左列/右列保留数字串(none 保留)。
  void _coerceRows() {
    final inLv = _levelsFor(_effortIn);
    final outLv = _levelsFor(_effortFormat);
    String pick(List<String> lv, String want) {
      if (lv.contains(want)) return want;
      return lv.length > 1 ? lv[1] : (lv.isNotEmpty ? lv[0] : want);
    }

    for (final r in _effortRows) {
      if (_entryCategory == 'budget') {
        if (int.tryParse(r.name.trim()) == null) r.name = '1024';
      } else if (inLv.isNotEmpty) {
        r.name = pick(inLv, r.name);
      }
      if (_upstreamCategory == 'budget') {
        final v = r.value.trim();
        if (v.toLowerCase() != 'none' && int.tryParse(v) == null) {
          r.value = '4096';
        }
      } else if (outLv.isNotEmpty) {
        r.value = pick(outLv, r.value);
      }
    }
  }

  /// 新增一行的默认值:左列取下游词表第一个非 none 档(budget 类给阈值
  /// 样例),右列取上游词表第一个非 none 档(budget 类给预算样例)。
  _EffortRow _newRow() {
    String firstTier(List<String> lv) => lv.length > 1 ? lv[1] : (lv.isNotEmpty ? lv[0] : '');
    return _EffortRow(
      _entryCategory == 'budget' ? '2048' : firstTier(_levelsFor(_effortIn)),
      _upstreamCategory == 'budget' ? '4096' : firstTier(_levelsFor(_effortFormat)),
    );
  }

  Widget _effortSection() {
    final rows = _effortRows;
    final inBudget = _entryCategory == 'budget';
    final outBudget = _upstreamCategory == 'budget';
    final inVocab = _levelsFor(_effortIn);
    final outVocab = _levelsFor(_effortFormat);
    final upstreamField = LabeledField(
      key: const ValueKey('model-effort-format-field'),
      label: '上游格式',
      hint: '读出的规范档按此形态写回体里发上游（决定上行字段结构）',
      child: StyledDropdownFormField(
        key: const ValueKey('model-effort-format'),
        value: _effortFormat,
        decoration: const InputDecoration(border: OutlineInputBorder()),
        options: _upstreamFormats,
        onChanged: (v) => setState(() {
          _effortFormat = v ?? _upstreamFormats.first;
          _coerceRows();
        }),
      ),
    );
    return CollapsibleSection(
      icon: Icons.psychology_outlined,
      title: '推理档',
      subtitle: _effortSubtitle,
      initiallyExpanded: true,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          // 总开关独占第一排(与上下文限制区同款):关闭=不读不写不剥离,
          // 其余字段常驻显示,值保留供重新开启即用。
          LabeledField(
            key: const ValueKey('model-effort-enabled-field'),
            label: '推理档转换',
            hint: '关闭后转发面不读不写不剥离,对话页选档不落笔',
            child: SizedBox(
              height: 40,
              child: Row(
                children: [
                  Transform.translate(
                    offset: const Offset(-4, 0),
                    child: Switch(
                      key: const ValueKey('model-effort-enabled'),
                      value: _effortEnabled,
                      materialTapTargetSize: MaterialTapTargetSize.shrinkWrap,
                      onChanged: (v) => setState(() => _effortEnabled = v),
                    ),
                  ),
                  const SizedBox(width: 8),
                  Text(
                    _effortEnabled ? '已开启' : '已关闭',
                    style: const TextStyle(fontSize: 12.5),
                  ),
                ],
              ),
            ),
          ),
          const SizedBox(height: 18),
          // 双端声明:下游格式(harness 送进来的形态)→ 上游格式(写出去的形态)。
          // 词表全量罗列,跨族组合合法;菜单项即格式名,无需 labelOf。
          FormRow2(
            LabeledField(
              key: const ValueKey('model-effort-in-field'),
              label: '下游格式',
              hint: '体里已有的档位字段形态：网关按它读出规范档并剥掉原键',
              child: StyledDropdownFormField(
                key: const ValueKey('model-effort-in'),
                value: _effortIn,
                decoration: const InputDecoration(border: OutlineInputBorder()),
                options: _entryFormats,
                onChanged: (v) => setState(() {
                  _effortIn = v ?? _entryFormats.first;
                  _coerceRows();
                }),
              ),
            ),
            upstreamField,
          ),
          const SizedBox(height: 18),
          // 关思考落定:仅 anthropic 族载档上游出现(anthropic_off 本身即
          // 关思考,无需填;openai/gemini 族忽略 effort_off)。
          if (_offRelevant) ...[
            LabeledField(
              key: const ValueKey('model-effort-off-field'),
              label: '关思考落定（none 档怎么写）',
              hint: 'disabled 标准 / between_tools（Sonnet 5.5）/ 不写（上游思考恒开）',
              child: StyledDropdownFormField(
                key: const ValueKey('model-effort-off-policy'),
                value: _effortOff,
                decoration: const InputDecoration(border: OutlineInputBorder()),
                options: _effortOffs,
                labelOf: _offLabel,
                onChanged: (v) => setState(() => _effortOff = v ?? ''),
              ),
            ),
            const SizedBox(height: 18),
          ],
          // 无档位类上游(anthropic_off):模型思考恒关,剥掉下游档位字段后
          // 按关思考形态发上游,无映射表可填;下游格式仍用于读档与剥键。
          if (_offCategory)
            Text(
              '无档位：剥掉下游档位字段后按关思考形态（thinking disabled）发上游，命中任何档位都关思考',
              style: TextStyle(
                fontSize: 12,
                color: Theme.of(context).hintColor,
              ),
            )
          else
            LabeledField(
              key: const ValueKey('model-effort-mode-field'),
              label: '映射表',
              hint: inBudget
                  ? '下游送预算数时按「不超过左值的最大行」匹配，取右列上行；左值按从小到大排列。右列 none=关思考（按上方落定形态写出）'
                  : '每行=下游档→上行值；none=无思考，两端都可选，写上游时按该格式的关思考形态适配（openai 族原样写 none，anthropic 族按关思考落定，gemini 不落字段）；词表内但未声明的档按就低矫正，词表外的未知值取声明行排除 none 后的中间行（4 行选第 2、3 行选第 2、5 行选第 3）',
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  // 列头:左=下游档(budget 类=预算上限),右=上行值。
                  Row(
                    children: [
                      const SizedBox(width: 28),
                      const SizedBox(width: 8),
                      Expanded(
                        child: Text(
                          inBudget ? '下游预算上限（tokens）' : '下游档（$_effortIn）',
                          style: TextStyle(
                            fontSize: 12,
                            color: Theme.of(context).hintColor,
                          ),
                        ),
                      ),
                      const SizedBox(width: 24),
                      Expanded(
                        child: Text(
                          outBudget ? '上行预算（tokens 或 none）' : '上行档（$_effortFormat）',
                          style: TextStyle(
                            fontSize: 12,
                            color: Theme.of(context).hintColor,
                          ),
                        ),
                      ),
                      const SizedBox(width: 48),
                    ],
                  ),
                  const SizedBox(height: 4),
                  for (var i = 0; i < rows.length; i++)
                    Padding(
                      padding: const EdgeInsets.only(bottom: 8),
                      child: Row(
                        children: [
                          SizedBox(
                            width: 28,
                            child: Text(
                              '${i + 1}',
                              textAlign: TextAlign.center,
                              style: TextStyle(
                                fontSize: 12.5,
                                color: Theme.of(context).hintColor,
                              ),
                            ),
                          ),
                          const SizedBox(width: 8),
                          // 左列:下游档(budget 类=阈值数字编辑框)。
                          Expanded(
                            child: inBudget
                                ? TextFormField(
                                    key: rows[i].nameKey,
                                    initialValue: rows[i].name,
                                    keyboardType: TextInputType.number,
                                    decoration: const InputDecoration(
                                      hintText: '阈值（tokens）',
                                    ),
                                    onChanged: (v) =>
                                        setState(() => rows[i].name = v),
                                  )
                                : StyledDropdownFormField(
                                    key: ValueKey('model-effort-name-$i'),
                                    value: rows[i].name.isEmpty
                                        ? _options(inVocab, '').firstOrNull
                                        : rows[i].name,
                                    options: _options(inVocab, rows[i].name),
                                    onChanged: (v) =>
                                        setState(() => rows[i].name = v ?? ''),
                                  ),
                          ),
                          const SizedBox(width: 4),
                          const Text('→'),
                          const SizedBox(width: 4),
                          // 右列:上行值(budget 类=token 数字或 none 编辑框)。
                          Expanded(
                            child: outBudget
                                ? TextFormField(
                                    key: rows[i].valueKey,
                                    initialValue: rows[i].value,
                                    decoration: const InputDecoration(
                                      hintText: '数字或 none',
                                    ),
                                    onChanged: (v) =>
                                        setState(() => rows[i].value = v),
                                  )
                                : StyledDropdownFormField(
                                    key: ValueKey('model-effort-value-$i'),
                                    value: rows[i].value.isEmpty
                                        ? _options(outVocab, '').firstOrNull
                                        : rows[i].value,
                                    options: _options(outVocab, rows[i].value),
                                    onChanged: (v) =>
                                        setState(() => rows[i].value = v ?? ''),
                                  ),
                          ),
                          IconButton(
                            key: ValueKey('model-effort-del-$i'),
                            tooltip: '删除',
                            icon: const Icon(Icons.remove_circle_outline),
                            onPressed: () =>
                                setState(() => _effortRows.removeAt(i)),
                          ),
                        ],
                      ),
                    ),
                  Align(
                    alignment: Alignment.centerLeft,
                    child: TextButton.icon(
                      key: const ValueKey('model-effort-add'),
                      onPressed: () => setState(() => _effortRows.add(_newRow())),
                      icon: const Icon(Icons.add, size: 16),
                      label: const Text('添加映射'),
                    ),
                  ),
                ],
              ),
            ),
        ],
      ),
    );
  }

  // ── 附加参数(默认/覆盖 JSON)──

  bool get _hasParams =>
      (_source?.defaults.isNotEmpty ?? false) ||
      (_source?.overrides.isNotEmpty ?? false);

  Widget _paramsSection() {
    return CollapsibleSection(
      icon: Icons.data_object_outlined,
      title: '附加参数',
      subtitle: '默认参数 · 覆盖参数（JSON）',
      // 已配参数时默认展开,新建空配置默认收起。
      initiallyExpanded: _hasParams,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          JsonField(
            key: const ValueKey('model-defaults'),
            label: '默认参数',
            helper: '调用方未提供该键时生效',
            controller: _defaults,
            onValidityChanged: (ok) => setState(() => _defaultsValid = ok),
          ),
          const SizedBox(height: 22),
          JsonField(
            key: const ValueKey('model-overrides'),
            label: '覆盖参数',
            helper: '强制值，优先级最高',
            controller: _overrides,
            onValidityChanged: (ok) => setState(() => _overridesValid = ok),
          ),
        ],
      ),
    );
  }
}

/// 一行映射对(2026-10-11 行式映射对):name=下游档(下游 budget 类时为
/// 阈值数字串),value=上行值(上游 budget 类时为 token 数字串或 none)。
/// 纯字符串态(下拉选中值/编辑框 onChanged 即写回),编辑框用 initialValue
/// + 行级 UniqueKey 保编辑态(增删行不按位置复用)。
class _EffortRow {
  _EffortRow([this.name = '', this.value = '']);

  String name;
  String value;
  final nameKey = UniqueKey();
  final valueKey = UniqueKey();
}

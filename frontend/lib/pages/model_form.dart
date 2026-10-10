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

  // 推理档=数字映射:档位是 0..N 的数字,0 档固定为「关闭思考」(上行值
  // none),由常驻开关决定是否提供;1..N 档每行只填一个 effort 值,行号即
  // 档位。对话页与下游按数字选档,上行发映射的值。没有自动跟随上游的模式;
  // 编辑/拷贝存量自动档(null)的模型时用当前有效列表的值预填并默认开,
  // 保存即落为显式声明(旧数据的中文名直接废弃,按行号重排)。
  late final ({bool off, List<_EffortRow> rows}) _effortInit = _initialEffort();
  late bool _disableThinking = _effortInit.off;
  late final List<_EffortRow> _effortRows = _effortInit.rows;
  // effort 写入格式:空=协议内置映射(按出站协议选字段);非空=显式格式
  // 压过协议外形,承接「协议外壳+自家字段」的厂商差异。存量旧值(四协议名)
  // 读时归一到双端词表,下次保存即迁移(与后端 NormalizeFormat 同表)。
  late String _effortFormat = _normalizeFormat(_source?.effortFormat ?? '');
  // 下游格式(effort_in):空=auto=现状(体里有 reasoning_level 就走它,对话页
  // 选档走规范档,透传体不解析不剥离);非空=显式声明 harness 送进来的形态。
  // 下游词表不收 gemini——存量 gemini 入口值读时回落 auto(下游适配已删)。
  late String _effortIn = _entryInit(_source?.effortIn ?? '');
  // 0 档在 anthropic 族上游的关思考落定:空=disabled / between_tools / omit。
  late String _effortOff = _source?.effortOff ?? '';
  // 推理档转换总开关:false=转发面不读不写不剥离,对话页选档不落笔;
  // 其余推理档字段常驻显示(值保留,重新开启即用)。
  late bool _effortEnabled = _source?.effortEnabled ?? true;

  ({bool off, List<_EffortRow> rows}) _initialEffort() {
    final source = _source;
    if (source == null) return (off: true, rows: <_EffortRow>[]);
    final entries = source.efforts ?? source.effortsEffective;
    final budgets = source.effortBudgets;
    return (
      // 显式声明按原值判断;存量自动(null)默认开,与新建一致。
      off: source.efforts == null || entries.any((e) => e.value == 'none'),
      rows: [
        for (final e in entries)
          if (e.value != 'none')
            _EffortRow(e.value, budgets[e.value]?.toString() ?? ''),
      ],
    );
  }

  bool _defaultsValid = true;
  bool _overridesValid = true;
  bool _busy = false;

  bool get _isEdit => widget.editing != null;

  @override
  void initState() {
    super.initState();
    _protocol = _source?.protocol ?? _protocols.firstOrNull;
  }

  @override
  void dispose() {
    _id.dispose();
    _nativeModel.dispose();
    _contextWindow.dispose();
    _defaults.dispose();
    _overrides.dispose();
    _compactThreshold.dispose();
    for (final r in _effortRows) {
      r.dispose();
    }
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
                      onChanged: (v) =>
                          setState(() => _compactMode = v ? 'error' : 'passive'),
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

  // ── 推理档(关闭思考开关 + 显式名+值档位,无自动模式)──

  /// 收集提交条目:档位数字即条目名——0 档固定 {0, none}(关闭思考开关
  /// 开时),1..N 档按行号命名 {行号, 值};空值行视为未填丢弃(其后行档号
  /// 按当前位置算,与界面行标一致)。开关关且删掉所有行即声明不支持。
  List<EffortEntry> get _efforts {
    return [
      if (_disableThinking) const EffortEntry(name: '0', value: 'none'),
      for (var i = 0; i < _effortRows.length; i++)
        if (_effortRows[i].value.text.trim().isNotEmpty)
          EffortEntry(
            name: '${i + 1}',
            value: _effortRows[i].value.text.trim(),
          ),
    ];
  }

  String get _effortSubtitle {
    final levels = [
      if (_disableThinking) '0·关闭思考',
      for (var i = 0; i < _effortRows.length; i++)
        if (_effortRows[i].value.text.trim().isNotEmpty)
          '${i + 1}·${_effortRows[i].value.text.trim()}',
    ];
    final base = levels.isEmpty ? '不支持' : levels.join(' / ');
    // 默认(双端均 auto)保持纯档位摘要;仅在显式声明入口/上游格式时追加,
    // 让折叠标题一眼看出转换方向。
    if (_effortIn.isEmpty && _effortFormat.isEmpty) return base;
    final inName = _effortIn.isEmpty ? 'auto' : _normalizeFormat(_effortIn);
    final upName = _effortFormat.isEmpty ? '协议内置' : _normalizeFormat(_effortFormat);
    return '$base · 下游 $inName → 上游 $upName';
  }

  /// 上游格式词表(value 与后端 effort.FormatXxx 枚举一致):八种,无 auto 项——
  /// auto(协议内置)不可选;存量 auto 模型的 value='' 不入菜单,触发器照常
  /// 显示 auto 标签,菜单无勾选项。effort_index 已废:
  /// reasoning_level 数字档是网关扩展字段,只走 auto 路径。
  static const _effortFormats = [
    'openai_chat',
    'openai_responses',
    'anthropic_effort',
    'anthropic_budget',
    'anthropic_adaptive',
    'anthropic_off',
    'gemini_level',
    'gemini_budget',
  ];

  /// 入口(下游)格式词表:六种,无 auto 项(不可选,存量 auto 值仅显示),
  /// 不收 gemini——gemini 下游适配已删,与后端 effort.ValidEntryFormat 同集。
  static const _entryFormats = [
    'openai_chat',
    'openai_responses',
    'anthropic_effort',
    'anthropic_budget',
    'anthropic_adaptive',
    'anthropic_off',
  ];

  /// 存量旧值(四协议名)→ 双端词表别名,与后端 effort.NormalizeFormat 同表。
  static String _normalizeFormat(String f) => switch (f) {
    'chat_completions' => 'openai_chat',
    'responses' => 'openai_responses',
    'anthropic' => 'anthropic_effort',
    'gemini' => 'gemini_level',
    _ => f,
  };

  /// 入口值初始化:归一后若不在入口词表(如存量 gemini)回落 auto(空串)——
  /// auto 只作存量值显示(不入菜单),StyledDropdown 允许 value 不在 options。
  static String _entryInit(String f) {
    final n = _normalizeFormat(f);
    return _entryFormats.contains(n) ? n : '';
  }

  /// 上游格式标签(auto=协议内置)。
  static String _formatLabel(String f) => switch (_normalizeFormat(f)) {
    'openai_chat' => 'openai_chat · 顶层 reasoning_effort',
    'openai_responses' => 'openai_responses · 嵌套 reasoning.effort',
    'anthropic_effort' => 'anthropic_effort · output_config.effort',
    'anthropic_budget' => 'anthropic_budget · thinking enabled + budget_tokens',
    'anthropic_adaptive' => 'anthropic_adaptive · thinking adaptive + output_config.effort',
    'anthropic_off' => 'anthropic_off · thinking disabled/between_tools（只承载关思考）',
    'gemini_level' => 'gemini_level · thinkingConfig.thinkingLevel（大写）',
    'gemini_budget' => 'gemini_budget · thinkingConfig.thinkingBudget（数字）',
    _ => '协议内置（跟随出站协议，与现状逐字一致）',
  };

  /// 入口格式标签:auto 文案与上游不同(强调现状透传不解析不剥离)。
  static String _entryFormatLabel(String f) => f.isEmpty
      ? 'auto=现状（reasoning_level 数字档 + 对话页选档，透传体不解析不剥离）'
      : _formatLabel(f);

  /// 关思考落定选项与标签(0 档在 anthropic 族上游怎么写)。
  static const _effortOffs = ['', 'between_tools', 'omit'];
  static String _offLabel(String o) => switch (o) {
    'between_tools' => 'between_tools（Sonnet 5.5 顶替 disabled）',
    'omit' => '不写（上游思考恒开，0 档仅靠不声明承担）',
    _ => 'disabled（标准：thinking 改写为 disabled）',
  };

  /// 显式选定的上游格式(归一后)。auto(空)不算「选定配对」——转发面回落
  /// 协议内置写法,与现状逐字一致,不触发任何配对专属字段。
  String get _upstreamFormat => _normalizeFormat(_effortFormat);

  /// 关思考落定:仅在显式选定 anthropic 族上游格式后出现。auto 上游用内置
  /// disabled 落定(现状),无需也不展示该下拉;其余格式忽略 effort_off。
  bool get _offRelevant => const {
    'anthropic_effort',
    'anthropic_budget',
    'anthropic_adaptive',
    'anthropic_off',
  }.contains(_upstreamFormat);

  /// 预算数输入:仅在显式选定预算类上游格式(anthropic_budget/gemini_budget)
  /// 后出现。auto 永不落到预算族,故未选定配对时无此列。
  bool get _budgetFamily => const {
    'anthropic_budget',
    'gemini_budget',
  }.contains(_upstreamFormat);

  /// 收集预算覆盖:档位值→正整数;留空/非正/非数视为用内置映射,不入表。
  Map<String, int> get _effortBudgets {
    final out = <String, int>{};
    for (final r in _effortRows) {
      final v = r.value.text.trim();
      final b = int.tryParse(r.budget.text.trim());
      if (v.isNotEmpty && b != null && b > 0) out[v] = b;
    }
    return out;
  }

  Widget _effortSection() {
    final rows = _effortRows;
    final budgetFamily = _budgetFamily;
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
          FormRow2(
            LabeledField(
              key: const ValueKey('model-effort-in-field'),
              label: '下游格式',
              hint: '客户端/harness 用哪种形态把档位送进来',
              child: StyledDropdownFormField(
                key: const ValueKey('model-effort-in'),
                value: _effortIn,
                decoration: const InputDecoration(border: OutlineInputBorder()),
                options: _entryFormats,
                labelOf: _entryFormatLabel,
                onChanged: (v) => setState(() => _effortIn = v ?? ''),
              ),
            ),
            LabeledField(
              key: const ValueKey('model-effort-format-field'),
              label: '上游格式',
              hint: '空=协议内置，按出站协议落定（与现状一致）',
              child: StyledDropdownFormField(
                key: const ValueKey('model-effort-format'),
                value: _effortFormat,
                decoration: const InputDecoration(border: OutlineInputBorder()),
                options: _effortFormats,
                labelOf: _formatLabel,
                onChanged: (v) => setState(() => _effortFormat = v ?? ''),
              ),
            ),
          ),
          const SizedBox(height: 18),
          // 关思考落定:仅 anthropic 族上游出现(其余格式忽略 effort_off)。
          if (_offRelevant) ...[
            LabeledField(
              key: const ValueKey('model-effort-off-field'),
              label: '关思考落定（0 档怎么写）',
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
          LabeledField(
            key: const ValueKey('model-effort-mode-field'),
            label: '档位表',
            hint: budgetFamily
                ? '行号即档号；预算列留空=用内置映射（low 1024 / medium 4000 / high 10000 / xhigh 20000 / max 32000），上行自动钳到 < max_tokens'
                : '行号即档号；0 档固定为关闭思考（上行值 none），由下方开关决定是否提供',
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                // 与档位行同高(输入框实测 40),序号行距才一致;Switch 收缩包裹并
                // 左移抵掉内置 4px 水平内边距,轨道左缘才能对齐输入框列
                SizedBox(
                  height: 40,
                  child: Row(
                    children: [
                      SizedBox(
                        width: 28,
                        child: Text(
                          '0',
                          textAlign: TextAlign.center,
                          style: TextStyle(
                            fontSize: 12.5,
                            color: Theme.of(context).hintColor,
                          ),
                        ),
                      ),
                      const SizedBox(width: 8),
                      Transform.translate(
                        offset: const Offset(-4, 0),
                        child: Switch(
                          key: const ValueKey('model-effort-off'),
                          value: _disableThinking,
                          materialTapTargetSize: MaterialTapTargetSize.shrinkWrap,
                          onChanged: (v) => setState(() => _disableThinking = v),
                        ),
                      ),
                      const SizedBox(width: 8),
                      const Expanded(
                        child: Text('关闭思考', style: TextStyle(fontSize: 12.5)),
                      ),
                    ],
                  ),
                ),
                const SizedBox(height: 8),
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
                        Expanded(
                          child: TextFormField(
                            key: ValueKey('model-effort-value-$i'),
                            controller: rows[i].value,
                            decoration: const InputDecoration(hintText: '值（发上游）'),
                          ),
                        ),
                        // 预算列仅预算类上游格式出现:档位值→预算 token 覆盖。
                        if (budgetFamily) ...[
                          const SizedBox(width: 8),
                          SizedBox(
                            width: 132,
                            child: TextFormField(
                              key: ValueKey('model-effort-budget-$i'),
                              controller: rows[i].budget,
                              keyboardType: TextInputType.number,
                              decoration: const InputDecoration(
                                hintText: '预算（留空=内置）',
                              ),
                            ),
                          ),
                        ],
                        IconButton(
                          key: ValueKey('model-effort-del-$i'),
                          tooltip: '删除',
                          icon: const Icon(Icons.remove_circle_outline),
                          onPressed: () => setState(() {
                            final r = _effortRows.removeAt(i);
                            r.dispose();
                          }),
                        ),
                      ],
                    ),
                  ),
                Align(
                  alignment: Alignment.centerLeft,
                  child: TextButton.icon(
                    key: const ValueKey('model-effort-add'),
                    onPressed: () => setState(() => _effortRows.add(_EffortRow())),
                    icon: const Icon(Icons.add, size: 16),
                    label: const Text('添加档位'),
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

/// 一行自定义推理档(行号即档位),值持一个控制器(动态增删行需要稳定的
/// 编辑态,不能用 initialValue 靠位置复用)。budget 是预算 token 覆盖,
/// 仅预算类上游格式(anthropic_budget/gemini_budget)在界面展示与生效。
class _EffortRow {
  _EffortRow([String value = '', String budget = ''])
    : value = TextEditingController(text: value),
      budget = TextEditingController(text: budget);

  final TextEditingController value;
  final TextEditingController budget;

  void dispose() {
    value.dispose();
    budget.dispose();
  }
}

import 'package:flutter/material.dart';

import '../api_client.dart';
import '../models.dart';
import '../ui/collapsible_section.dart';
import '../ui/feedback.dart';
import '../ui/form_page.dart';
import '../ui/json_field.dart';
import '../ui/provider_avatar.dart';
import '../ui/styled_dropdown.dart';

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
      text: widget.editing?.id ??
          (widget.copyFrom != null ? '${widget.copyFrom!.id}-copy' : ''));
  late final TextEditingController _nativeModel =
      TextEditingController(text: _source?.nativeModel ?? '');
  // 上下文窗口按 k 单位录入/回显(1k = 1000 tokens),提交时换回 token 数
  late final TextEditingController _contextWindow = TextEditingController(
      text: tokensToK(_source?.contextWindow ?? 0));
  late final TextEditingController _defaults = TextEditingController(
      text: prettyJson(_source?.defaults ?? const {}));
  late final TextEditingController _overrides = TextEditingController(
      text: prettyJson(_source?.overrides ?? const {}));

  // 上下文压缩策略:模式三选一(passive 元数据/error 客户端压缩/
  // auto 上游压缩);阈值按百分比录入(85 = 窗口的 85%),提交时换回比例
  late String _compactMode =
      (_source?.compact['mode'] as String?) ?? 'passive';
  late final TextEditingController _compactThreshold = TextEditingController(
      text: _compactThresholdText(_source?.compact));
  late final TextEditingController _compactKeepTurns = TextEditingController(
      text: '${_source?.compact['keep_turns'] ?? 6}');

  static String _compactThresholdText(Map<String, dynamic>? compact) {
    final t = compact?['threshold'];
    if (t is num) return '${(t * 100).round()}';
    return '85';
  }

  late String _account = _source?.account ?? widget.initialAccount;
  String? _protocol;
  // 启停由列表行开关控制,表单不再展示;编辑/拷贝时沿用原值提交,新建默认启用
  late final bool _enabled = _source?.enabled ?? true;

  // 推理档支持列表:只允许显式声明(名+值动态行,空列表即声明不支持),
  // 没有自动跟随上游的模式。「关闭思考」是常驻开关(上行值 none),开即在
  // 有效列表最前面加一条 {关闭思考, none},新建默认开;编辑/拷贝存量
  // 自动档(null)的模型时用当前有效列表预填并默认开,保存即落为显式声明。
  late final ({bool off, List<_EffortRow> rows}) _effortInit = _initialEffort();
  late bool _disableThinking = _effortInit.off;
  late final List<_EffortRow> _effortRows = _effortInit.rows;

  ({bool off, List<_EffortRow> rows}) _initialEffort() {
    final source = _source;
    if (source == null) return (off: true, rows: <_EffortRow>[]);
    final entries = source.efforts ?? source.effortsEffective;
    return (
      // 显式声明按原值判断;存量自动(null)默认开,与新建一致。
      off: source.efforts == null || entries.any((e) => e.value == 'none'),
      rows: [
        for (final e in entries)
          if (e.value != 'none') _EffortRow(e.name, e.value),
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
    _compactKeepTurns.dispose();
    for (final r in _effortRows) {
      r.dispose();
    }
    super.dispose();
  }

  /// 所选账号 → 其 provider → 支持协议。
  List<String> get _protocols {
    final account =
        widget.accounts.where((a) => a.name == _account).firstOrNull;
    if (account == null) return const [];
    final spec = widget.providers
        .where((p) => p.id == account.providerId)
        .firstOrNull;
    return spec?.protocols ?? const [];
  }

  /// 卡顶居中头像取所选账号的提供商,随账号切换联动。
  String get _avatarProvider => widget.accounts
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

  /// 组装 compact JSON 提交:模式必带;阈值在 error/auto 下生效;
  /// 保留轮数仅 auto 使用。缺项回落服务端全局默认。
  Map<String, dynamic> _buildCompact() {
    final out = <String, dynamic>{'mode': _compactMode};
    if (_compactMode != 'passive') {
      final pct = double.tryParse(_compactThreshold.text.trim());
      if (pct != null) out['threshold'] = pct / 100;
    }
    if (_compactMode == 'auto') {
      final turns = int.tryParse(_compactKeepTurns.text.trim());
      if (turns != null) out['keep_turns'] = turns;
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
          account: _account,
          nativeModel: _nativeModel.text.trim(),
          protocol: _protocol,
          contextWindow: kToTokens(_contextWindow.text),
          defaults: defaults!,
          overrides: overrides!,
          compact: _buildCompact(),
          efforts: _efforts,
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
    return FormPage(
      title: _isEdit
          ? '编辑模型 ${widget.editing!.id}'
          : widget.copyFrom != null
              ? '拷贝模型 ${widget.copyFrom!.id}'
              : '新建模型',
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
                labelOf: (name) {
                  final a = widget.accounts
                      .where((a) => a.name == name)
                      .firstOrNull;
                  return a == null ? name : '$name (${a.providerId})';
                },
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
          // 编辑模式下模型标识不可改,直接不渲染该字段(标题已含标识)
          if (!_isEdit) ...[
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
                validator: (v) => (v == null || v.trim().isEmpty)
                    ? '模型标识不能为空'
                    : null,
              ),
            ),
          ],
          const SizedBox(height: 20),
          LabeledField(
            label: '上游模型名',
            child: TextFormField(
              key: const ValueKey('model-native'),
              controller: _nativeModel,
              decoration: const InputDecoration(
                hintText: 'kimi-k2-turbo',
                border: OutlineInputBorder(),
              ),
              validator: (v) => (v == null || v.trim().isEmpty)
                  ? '上游模型名不能为空'
                  : null,
            ),
          ),
        ],
      ),
    );
  }

  // ── 上下文限制(窗口/压缩策略/触发阈值/保留轮数)──

  String get _contextSubtitle {
    final window = _contextWindow.text.trim();
    final mode = switch (_compactMode) {
      'error' => '客户端压缩',
      'auto' => '上游压缩',
      _ => '元数据',
    };
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
          // 下拉与输入框等高(StyledDropdown 已对齐 TextFormField),并排双列。
          FormRow2(
            LabeledField(
              label: '上下文窗口',
              child: TextFormField(
                key: const ValueKey('model-context'),
                controller: _contextWindow,
                keyboardType:
                    const TextInputType.numberWithOptions(decimal: true),
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
            // 上下文压缩:模式下拉常驻;阈值对 error/auto 生效;
            // 保留轮数仅 auto 使用
            LabeledField(
              key: const ValueKey('model-compact-mode-field'),
              label: '上下文压缩策略',
              child: StyledDropdownFormField(
                key: const ValueKey('model-compact-mode'),
                value: _compactMode,
                decoration: const InputDecoration(border: OutlineInputBorder()),
                options: const ['passive', 'error', 'auto'],
                labelOf: (m) => switch (m) {
                  'error' => '客户端压缩',
                  'auto' => '上游压缩',
                  _ => '元数据',
                },
                onChanged: (v) => setState(() => _compactMode = v!),
              ),
            ),
          ),
          if (_compactMode != 'passive') ...[
            const SizedBox(height: 20),
            FormRow2(
              LabeledField(
                key: const ValueKey('model-compact-threshold-field'),
                label: '触发阈值',
                hint: '估算输入超过窗口此比例时触发',
                child: TextFormField(
                  key: const ValueKey('model-compact-threshold'),
                  controller: _compactThreshold,
                  keyboardType:
                      const TextInputType.numberWithOptions(decimal: true),
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
              _compactMode == 'auto'
                  ? LabeledField(
                      key: const ValueKey('model-compact-keep-field'),
                      label: '保留最近轮数',
                      hint: '压缩后原样保留的最近对话轮数',
                      child: TextFormField(
                        key: const ValueKey('model-compact-keep'),
                        controller: _compactKeepTurns,
                        keyboardType: TextInputType.number,
                        decoration: const InputDecoration(
                          border: OutlineInputBorder(),
                        ),
                        validator: (v) {
                          final n = int.tryParse(v?.trim() ?? '');
                          if (n == null) return '请填写整数';
                          if (n < 1) return '至少保留 1 轮';
                          return null;
                        },
                      ),
                    )
                  : const SizedBox.shrink(),
            ),
          ],
        ],
      ),
    );
  }

  // ── 推理档(关闭思考开关 + 显式名+值档位,无自动模式)──

  /// 收集提交条目:关闭思考开关开时最前面固定一条 {关闭思考, none};
  /// 值是档位的身份(去重/上行都靠它),空值行视为未填丢弃;名留空由服务端
  /// 按值自动命名。开关关且删掉所有行即声明不支持。
  List<EffortEntry> get _efforts {
    return [
      if (_disableThinking) const EffortEntry(name: '关闭思考', value: 'none'),
      for (final r in _effortRows)
        if (r.value.text.trim().isNotEmpty)
          EffortEntry(name: r.name.text.trim(), value: r.value.text.trim()),
    ];
  }

  String get _effortSubtitle {
    final names = [
      if (_disableThinking) '关闭思考',
      for (final r in _effortRows)
        if (r.value.text.trim().isNotEmpty)
          r.name.text.trim().isNotEmpty
              ? r.name.text.trim()
              : r.value.text.trim(),
    ];
    if (names.isEmpty) return '不支持';
    return names.join(' / ');
  }

  Widget _effortSection() {
    final rows = _effortRows;
    return CollapsibleSection(
      icon: Icons.psychology_outlined,
      title: '推理档',
      subtitle: _effortSubtitle,
      initiallyExpanded: true,
      child: LabeledField(
        key: const ValueKey('model-effort-mode-field'),
        label: '支持档位',
        hint: '关闭思考开关=提供不思考选项（上行值 none）；每行一个档位：名是显示名（留空按值命名），值是发上游的档位字符串；开关关且删掉所有行即声明不支持推理档',
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Row(
              children: [
                Switch(
                  key: const ValueKey('model-effort-off'),
                  value: _disableThinking,
                  onChanged: (v) => setState(() => _disableThinking = v),
                ),
                const SizedBox(width: 8),
                const Expanded(
                  child: Text('关闭思考', style: TextStyle(fontSize: 12.5)),
                ),
              ],
            ),
            const SizedBox(height: 14),
            for (var i = 0; i < rows.length; i++)
              Padding(
                padding: const EdgeInsets.only(bottom: 8),
                child: Row(
                  children: [
                    Expanded(
                      child: TextFormField(
                        key: ValueKey('model-effort-name-$i'),
                        controller: rows[i].name,
                        decoration: const InputDecoration(
                          hintText: '名',
                        ),
                      ),
                    ),
                    const SizedBox(width: 8),
                    Expanded(
                      flex: 2,
                      child: TextFormField(
                        key: ValueKey('model-effort-value-$i'),
                        controller: rows[i].value,
                        decoration: const InputDecoration(
                          hintText: '值',
                        ),
                      ),
                    ),
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

/// 一行自定义推理档,名与值各持一个控制器(动态增删行需要稳定的
/// 编辑态,不能用 initialValue 靠位置复用)。
class _EffortRow {
  _EffortRow([String name = '', String value = ''])
      : name = TextEditingController(text: name),
        value = TextEditingController(text: value);

  final TextEditingController name;
  final TextEditingController value;

  void dispose() {
    name.dispose();
    value.dispose();
  }
}

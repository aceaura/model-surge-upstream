import 'package:flutter/material.dart';

import '../api_client.dart';
import '../models.dart';
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

  // 上下文压缩:模式三选一(passive 被动元数据/error 回错让客户端压缩/
  // auto 代理自动压缩);阈值按百分比录入(85 = 窗口的 85%),提交时换回比例
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
            FormRow2(
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
              LabeledField(
                label: '上下文窗口',
                child: TextFormField(
                  key: const ValueKey('model-context'),
                  controller: _contextWindow,
                  keyboardType:
                      const TextInputType.numberWithOptions(decimal: true),
                  decoration: const InputDecoration(
                    suffixText: 'k',
                    helperText: '单位 k(1k = 1000 tokens),0 表示未声明',
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
            ),
            const SizedBox(height: 20),
            // 上下文压缩:模式下拉常驻;阈值对 error/auto 生效;
            // 保留轮数仅 auto 使用
            FormRow2(
              LabeledField(
                key: const ValueKey('model-compact-mode-field'),
                label: '上下文压缩',
                child: StyledDropdownFormField(
                  key: const ValueKey('model-compact-mode'),
                  value: _compactMode,
                  decoration: const InputDecoration(border: OutlineInputBorder()),
                  options: const ['passive', 'error', 'auto'],
                  labelOf: (m) => switch (m) {
                    'error' => '返回错误（客户端自行压缩）',
                    'auto' => '代理自动压缩',
                    _ => '被动元数据（只记录不生效）',
                  },
                  onChanged: (v) => setState(() => _compactMode = v!),
                ),
              ),
              _compactMode != 'passive'
                  ? LabeledField(
                      key: const ValueKey('model-compact-threshold-field'),
                      label: '触发阈值',
                      child: TextFormField(
                        key: const ValueKey('model-compact-threshold'),
                        controller: _compactThreshold,
                        keyboardType: const TextInputType.numberWithOptions(
                            decimal: true),
                        decoration: const InputDecoration(
                          suffixText: '%',
                          helperText: '估算输入超过窗口此比例时触发',
                          border: OutlineInputBorder(),
                        ),
                        validator: (v) {
                          final n = double.tryParse(v?.trim() ?? '');
                          if (n == null) return '请填写数字';
                          if (n <= 0 || n > 100) return '须在 1–100 之间';
                          return null;
                        },
                      ),
                    )
                  : const SizedBox.shrink(),
            ),
            if (_compactMode == 'auto') ...[
              const SizedBox(height: 20),
              FormRow2(
                LabeledField(
                  key: const ValueKey('model-compact-keep-field'),
                  label: '保留最近轮数',
                  child: TextFormField(
                    key: const ValueKey('model-compact-keep'),
                    controller: _compactKeepTurns,
                    keyboardType: TextInputType.number,
                    decoration: const InputDecoration(
                      helperText: '压缩后原样保留的最近对话轮数',
                      border: OutlineInputBorder(),
                    ),
                    validator: (v) {
                      final n = int.tryParse(v?.trim() ?? '');
                      if (n == null) return '请填写整数';
                      if (n < 1) return '至少保留 1 轮';
                      return null;
                    },
                  ),
                ),
                const SizedBox.shrink(),
              ),
            ],
            const SizedBox(height: 26),
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
      ),
    );
  }
}

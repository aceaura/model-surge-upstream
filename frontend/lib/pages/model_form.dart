import 'package:flutter/material.dart';

import '../api_client.dart';
import '../models.dart';
import '../ui/feedback.dart';
import '../ui/json_field.dart';

/// 模型创建与编辑表单。协议候选来自所选账号对应 provider 的支持集，
/// 随账号切换联动，避免提交注定被服务端拒绝的组合。
class ModelForm extends StatefulWidget {
  const ModelForm({
    super.key,
    required this.client,
    required this.accounts,
    required this.providers,
    required this.initialAccount,
    this.editing,
  });

  final ApiClient client;
  final List<Account> accounts;
  final List<ProviderSpec> providers;
  final String initialAccount;
  final UpstreamModel? editing;

  @override
  State<ModelForm> createState() => _ModelFormState();
}

class _ModelFormState extends State<ModelForm> {
  final _formKey = GlobalKey<FormState>();
  late final TextEditingController _id =
      TextEditingController(text: widget.editing?.id ?? '');
  late final TextEditingController _nativeModel =
      TextEditingController(text: widget.editing?.nativeModel ?? '');
  late final TextEditingController _contextWindow = TextEditingController(
      text: (widget.editing?.contextWindow ?? 0).toString());
  late final TextEditingController _defaults = TextEditingController(
      text: prettyJson(widget.editing?.defaults ?? const {}));
  late final TextEditingController _overrides = TextEditingController(
      text: prettyJson(widget.editing?.overrides ?? const {}));

  late String _account = widget.editing?.account ?? widget.initialAccount;
  String? _protocol;
  late bool _enabled = widget.editing?.enabled ?? true;

  bool _defaultsValid = true;
  bool _overridesValid = true;
  bool _busy = false;

  bool get _isEdit => widget.editing != null;

  @override
  void initState() {
    super.initState();
    _protocol = widget.editing?.protocol ?? _protocols.firstOrNull;
  }

  @override
  void dispose() {
    _id.dispose();
    _nativeModel.dispose();
    _contextWindow.dispose();
    _defaults.dispose();
    _overrides.dispose();
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
          contextWindow: int.parse(_contextWindow.text.trim()),
          defaults: defaults!,
          overrides: overrides!,
          enabled: _enabled,
        );
      } else {
        await widget.client.createModel(
          id: _id.text.trim(),
          account: _account,
          nativeModel: _nativeModel.text.trim(),
          protocol: _protocol!,
          contextWindow: int.parse(_contextWindow.text.trim()),
          defaults: defaults!,
          overrides: overrides!,
          enabled: _enabled,
        );
      }
      if (!mounted) return;
      Navigator.of(context).pop(true);
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
    return AlertDialog(
      title: Text(_isEdit ? '编辑模型 ${widget.editing!.id}' : '新建模型'),
      content: SizedBox(
        width: 560,
        child: SingleChildScrollView(
          child: Form(
            key: _formKey,
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                DropdownButtonFormField<String>(
                  initialValue: _account,
                  decoration: const InputDecoration(
                    labelText: '账号',
                    border: OutlineInputBorder(),
                  ),
                  items: [
                    for (final a in widget.accounts)
                      DropdownMenuItem(
                        value: a.name,
                        child: Text('${a.name} (${a.providerId})'),
                      ),
                  ],
                  onChanged: _onAccountChanged,
                ),
                const SizedBox(height: 16),
                TextFormField(
                  controller: _id,
                  enabled: !_isEdit,
                  decoration: const InputDecoration(
                    labelText: '模型标识',
                    hintText: 'kimi-1/k2',
                    border: OutlineInputBorder(),
                  ),
                  validator: (v) =>
                      (v == null || v.trim().isEmpty) ? '模型标识不能为空' : null,
                ),
                const SizedBox(height: 16),
                TextFormField(
                  controller: _nativeModel,
                  decoration: const InputDecoration(
                    labelText: '上游模型名',
                    hintText: 'kimi-k2-turbo',
                    border: OutlineInputBorder(),
                  ),
                  validator: (v) =>
                      (v == null || v.trim().isEmpty) ? '上游模型名不能为空' : null,
                ),
                const SizedBox(height: 16),
                DropdownButtonFormField<String>(
                  key: ValueKey('protocol-$_account'),
                  initialValue: _protocol,
                  decoration: const InputDecoration(
                    labelText: '协议',
                    border: OutlineInputBorder(),
                  ),
                  items: [
                    for (final p in _protocols)
                      DropdownMenuItem(value: p, child: Text(p)),
                  ],
                  onChanged: (v) => setState(() => _protocol = v),
                  validator: (v) => v == null ? '请选择协议' : null,
                ),
                const SizedBox(height: 16),
                TextFormField(
                  controller: _contextWindow,
                  keyboardType: TextInputType.number,
                  decoration: const InputDecoration(
                    labelText: '上下文窗口',
                    helperText: '0 表示未声明',
                    border: OutlineInputBorder(),
                  ),
                  validator: (v) {
                    final n = int.tryParse(v?.trim() ?? '');
                    if (n == null) return '请填写整数';
                    if (n < 0) return '不能为负数';
                    return null;
                  },
                ),
                const SizedBox(height: 16),
                JsonField(
                  label: '默认参数',
                  helper: '调用方未提供该键时生效',
                  controller: _defaults,
                  onValidityChanged: (ok) =>
                      setState(() => _defaultsValid = ok),
                ),
                const SizedBox(height: 16),
                JsonField(
                  label: '覆盖参数',
                  helper: '强制值，优先级最高',
                  controller: _overrides,
                  onValidityChanged: (ok) =>
                      setState(() => _overridesValid = ok),
                ),
                SwitchListTile(
                  title: const Text('启用'),
                  value: _enabled,
                  onChanged: (v) => setState(() => _enabled = v),
                ),
              ],
            ),
          ),
        ),
      ),
      actions: [
        TextButton(
          onPressed: _busy ? null : () => Navigator.of(context).pop(false),
          child: const Text('取消'),
        ),
        BusyButton(
          busy: _busy,
          onPressed: _canSubmit ? _submit : null,
          child: Text(_isEdit ? '保存' : '创建'),
        ),
      ],
    );
  }
}

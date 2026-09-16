import 'package:flutter/material.dart';

import '../api_client.dart';
import '../models.dart';
import '../ui/feedback.dart';
import '../ui/header_editor.dart';

/// 账号创建与编辑表单。editing 非空时为编辑：密钥留空表示保留原凭据。
class AccountForm extends StatefulWidget {
  const AccountForm({
    super.key,
    required this.client,
    required this.providers,
    this.editing,
  });

  final ApiClient client;
  final List<ProviderSpec> providers;
  final Account? editing;

  @override
  State<AccountForm> createState() => _AccountFormState();
}

class _AccountFormState extends State<AccountForm> {
  final _formKey = GlobalKey<FormState>();
  late final TextEditingController _name =
      TextEditingController(text: widget.editing?.name ?? '');
  late final TextEditingController _apiKey = TextEditingController();
  late final TextEditingController _baseUrl =
      TextEditingController(text: widget.editing?.baseUrl ?? '');

  late String? _providerId = widget.editing?.providerId ??
      (widget.providers.isNotEmpty ? widget.providers.first.id : null);
  late Map<String, String> _headers = {...?widget.editing?.headers};
  late bool _enabled = widget.editing?.enabled ?? true;

  bool _busy = false;

  bool get _isEdit => widget.editing != null;

  @override
  void dispose() {
    _name.dispose();
    _apiKey.dispose();
    _baseUrl.dispose();
    super.dispose();
  }

  ProviderSpec? get _spec =>
      widget.providers.where((p) => p.id == _providerId).firstOrNull;

  Future<void> _submit() async {
    if (!(_formKey.currentState?.validate() ?? false)) return;
    setState(() => _busy = true);
    try {
      if (_isEdit) {
        await widget.client.updateAccount(
          name: widget.editing!.name,
          providerId: _providerId,
          apiKey: _apiKey.text.trim(),
          baseUrl: _baseUrl.text.trim(),
          headers: _headers,
          enabled: _enabled,
        );
      } else {
        await widget.client.createAccount(
          name: _name.text.trim(),
          providerId: _providerId!,
          apiKey: _apiKey.text.trim(),
          baseUrl: _baseUrl.text.trim(),
          headers: _headers,
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

  @override
  Widget build(BuildContext context) {
    final spec = _spec;
    return AlertDialog(
      title: Text(_isEdit ? '编辑账号 ${widget.editing!.name}' : '新建账号'),
      content: SizedBox(
        width: 520,
        child: SingleChildScrollView(
          child: Form(
            key: _formKey,
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                DropdownButtonFormField<String>(
                  initialValue: _providerId,
                  decoration: const InputDecoration(
                    labelText: '提供商',
                    border: OutlineInputBorder(),
                  ),
                  items: [
                    for (final p in widget.providers)
                      DropdownMenuItem(
                        value: p.id,
                        child: Text('${p.displayName} (${p.id})'),
                      ),
                  ],
                  onChanged: (v) => setState(() => _providerId = v),
                  validator: (v) => v == null ? '请选择提供商' : null,
                ),
                if (spec != null)
                  Padding(
                    padding: const EdgeInsets.only(top: 8),
                    child: Text('默认请求地址 ${spec.baseUrl}',
                        style: Theme.of(context).textTheme.bodySmall),
                  ),
                const SizedBox(height: 16),
                TextFormField(
                  controller: _name,
                  enabled: !_isEdit,
                  decoration: const InputDecoration(
                    labelText: '账号名',
                    hintText: 'kimi-1',
                    border: OutlineInputBorder(),
                  ),
                  validator: (v) =>
                      (v == null || v.trim().isEmpty) ? '账号名不能为空' : null,
                ),
                const SizedBox(height: 16),
                TextFormField(
                  controller: _apiKey,
                  obscureText: true,
                  decoration: InputDecoration(
                    labelText: '密钥',
                    hintText: _isEdit
                        ? '留空保留原密钥（当前 ${widget.editing!.maskedApiKey}）'
                        : null,
                    border: const OutlineInputBorder(),
                  ),
                  validator: (v) {
                    if (_isEdit) return null;
                    return (v == null || v.trim().isEmpty) ? '密钥不能为空' : null;
                  },
                ),
                const SizedBox(height: 16),
                TextFormField(
                  controller: _baseUrl,
                  decoration: const InputDecoration(
                    labelText: '请求地址覆盖（可选）',
                    hintText: '留空使用提供商默认地址',
                    border: OutlineInputBorder(),
                  ),
                  validator: (v) {
                    final t = v?.trim() ?? '';
                    if (t.isEmpty) return null;
                    if (!t.startsWith('http://') && !t.startsWith('https://')) {
                      return '需以 http:// 或 https:// 开头';
                    }
                    return null;
                  },
                ),
                const SizedBox(height: 16),
                HeaderEditor(
                  initial: _headers,
                  onChanged: (h) => _headers = h,
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
          onPressed: _submit,
          child: Text(_isEdit ? '保存' : '创建'),
        ),
      ],
    );
  }
}

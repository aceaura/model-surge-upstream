import 'package:flutter/material.dart';

import '../api_client.dart';
import '../models.dart';
import '../ui/feedback.dart';
import '../ui/header_editor.dart';
import '../ui/styled_dropdown.dart';

/// 账号创建与编辑表单。editing 非空时为编辑：密钥留空表示保留原凭据。
/// copyFrom 非空时为"拷贝创建":以该账号的配置预填(密钥不可见需重填),
/// 仍是新建语义。
class AccountForm extends StatefulWidget {
  const AccountForm({
    super.key,
    required this.client,
    required this.providers,
    this.editing,
    this.copyFrom,
  });

  final ApiClient client;
  final List<ProviderSpec> providers;
  final Account? editing;

  /// 拷贝来源:以其配置预填新建表单。
  final Account? copyFrom;

  @override
  State<AccountForm> createState() => _AccountFormState();
}

class _AccountFormState extends State<AccountForm> {
  final _formKey = GlobalKey<FormState>();
  late final TextEditingController _name = TextEditingController(
      text: widget.editing?.name ??
          (widget.copyFrom != null ? '${widget.copyFrom!.name}-copy' : ''));
  late final TextEditingController _apiKey = TextEditingController();
  late final TextEditingController _baseUrl = TextEditingController(
      text: widget.editing?.baseUrl ?? widget.copyFrom?.baseUrl ?? '');

  late String? _providerId = widget.editing?.providerId ??
      widget.copyFrom?.providerId ??
      (widget.providers.isNotEmpty ? widget.providers.first.id : null);
  late Map<String, String> _headers = {
    ...?widget.editing?.headers ?? widget.copyFrom?.headers
  };
  late bool _enabled =
      widget.editing?.enabled ?? widget.copyFrom?.enabled ?? true;

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
      title: Text(_isEdit
          ? '编辑账号 ${widget.editing!.name}'
          : widget.copyFrom != null
              ? '拷贝账号 ${widget.copyFrom!.name}'
              : '新建账号'),
      content: SizedBox(
        width: 680,
        child: SingleChildScrollView(
          child: Form(
            key: _formKey,
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                StyledDropdownFormField(
                  value: _providerId,
                  decoration: const InputDecoration(
                    labelText: '提供商',
                    border: OutlineInputBorder(),
                  ),
                  options: [for (final p in widget.providers) p.id],
                  labelOf: (id) {
                    final p = widget.providers
                        .where((p) => p.id == id)
                        .firstOrNull;
                    return p == null ? id : '${p.displayName} ($id)';
                  },
                  onChanged: (v) => setState(() => _providerId = v),
                  validator: (v) => v == null ? '请选择提供商' : null,
                ),
                if (spec != null)
                  Padding(
                    padding: const EdgeInsets.only(top: 8),
                    child: Text('默认请求地址 ${spec.baseUrl}',
                        style: Theme.of(context).textTheme.bodySmall),
                  ),
                // 编辑模式下账号名不可改,直接不渲染该字段(标题已含账号名)
                if (!_isEdit) ...[
                  const SizedBox(height: 16),
                  TextFormField(
                    controller: _name,
                    decoration: const InputDecoration(
                      labelText: '账号名',
                      hintText: 'kimi-1',
                      border: OutlineInputBorder(),
                    ),
                    validator: (v) =>
                        (v == null || v.trim().isEmpty) ? '账号名不能为空' : null,
                  ),
                ],
                const SizedBox(height: 16),
                TextFormField(
                  controller: _apiKey,
                  obscureText: true,
                  decoration: InputDecoration(
                    labelText: '密钥',
                    hintText: _isEdit
                        ? '留空保留原密钥（当前 ${widget.editing!.maskedApiKey}）'
                        : widget.copyFrom != null
                            ? '原密钥不可见（${widget.copyFrom!.maskedApiKey}），需重新填入'
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

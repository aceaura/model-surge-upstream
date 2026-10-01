import 'package:flutter/material.dart';

import '../api_client.dart';
import '../models.dart';
import '../ui/feedback.dart';
import '../ui/form_page.dart';
import '../ui/header_editor.dart';
import '../ui/provider_avatar.dart';
import '../ui/styled_dropdown.dart';

/// 账号创建与编辑整页表单(CC Switch 式:页内内联替换列表,不推根路由,
/// 侧边栏保持可见;不用居中弹窗)。
/// editing 非空时为编辑：密钥留空表示保留原凭据。
/// copyFrom 非空时为"拷贝创建":以该账号的配置预填(密钥不可见需重填),
/// 仍是新建语义。
class AccountForm extends StatefulWidget {
  const AccountForm({
    super.key,
    required this.client,
    required this.providers,
    required this.onDone,
    this.editing,
    this.copyFrom,
  });

  final ApiClient client;
  final List<ProviderSpec> providers;

  /// 表单收尾回调:true=已保存(宿主需重载列表),false=放弃修改。
  final ValueChanged<bool> onDone;
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

  late String? _providerId = widget.editing?.providerId ??
      widget.copyFrom?.providerId ??
      (widget.providers.isNotEmpty ? widget.providers.first.id : null);

  // 请求地址可编辑:默认取提供商默认地址,已存覆盖值时取覆盖值
  late final TextEditingController _baseUrl =
      TextEditingController(text: _initialBaseUrl());
  late Map<String, String> _headers = {
    ...?widget.editing?.headers ?? widget.copyFrom?.headers
  };
  // 启停由列表行的开关控制,表单不再展示;编辑/拷贝时沿用原值提交,
  // 新建默认启用
  late final bool _enabled =
      widget.editing?.enabled ?? widget.copyFrom?.enabled ?? true;

  bool _revealKey = false;
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

  String _defaultBaseUrlFor(String? providerId) =>
      widget.providers.where((p) => p.id == providerId).firstOrNull?.baseUrl ??
      '';

  String _initialBaseUrl() {
    final source = widget.editing ?? widget.copyFrom;
    if (source != null) {
      return source.baseUrl.isNotEmpty
          ? source.baseUrl
          : _defaultBaseUrlFor(source.providerId);
    }
    return _defaultBaseUrlFor(
        widget.providers.isNotEmpty ? widget.providers.first.id : null);
  }

  /// 换提供商时:地址仍是旧提供商默认值(用户没改过)就跟着换成新默认值,
  /// 用户改过则保留其输入。
  void _onProviderChanged(String? v) {
    setState(() {
      final oldDefault = _defaultBaseUrlFor(_providerId);
      if (oldDefault.isNotEmpty && _baseUrl.text.trim() == oldDefault) {
        _baseUrl.text = _defaultBaseUrlFor(v);
      }
      _providerId = v;
    });
  }

  Future<void> _submit() async {
    if (!(_formKey.currentState?.validate() ?? false)) return;
    setState(() => _busy = true);
    try {
      // 与提供商默认一致即视为不覆盖,保持"跟随提供商"语义
      final url = _baseUrl.text.trim();
      final baseUrl = url == (_spec?.baseUrl ?? '') ? '' : url;
      if (_isEdit) {
        await widget.client.updateAccount(
          name: widget.editing!.name,
          providerId: _providerId,
          apiKey: _apiKey.text.trim(),
          baseUrl: baseUrl,
          headers: _headers,
          enabled: _enabled,
        );
      } else {
        await widget.client.createAccount(
          name: _name.text.trim(),
          providerId: _providerId!,
          apiKey: _apiKey.text.trim(),
          baseUrl: baseUrl,
          headers: _headers,
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

  Widget _providerDropdown() => StyledDropdownFormField(
        key: const ValueKey('account-provider'),
        value: _providerId,
        decoration: const InputDecoration(border: OutlineInputBorder()),
        options: [for (final p in widget.providers) p.id],
        labelOf: (id) {
          final p = widget.providers.where((p) => p.id == id).firstOrNull;
          return p == null ? id : '${p.displayName} ($id)';
        },
        onChanged: _onProviderChanged,
        validator: (v) => v == null ? '请选择提供商' : null,
      );

  @override
  Widget build(BuildContext context) {
    return FormPage(
      title: _isEdit
          ? '编辑账号 ${widget.editing!.name}'
          : widget.copyFrom != null
              ? '拷贝账号 ${widget.copyFrom!.name}'
              : '新建账号',
      avatar: ProviderAvatar(providerId: _providerId ?? '?', size: 56),
      onCancel: () => widget.onDone(false),
      onSubmit: _submit,
      submitLabel: _isEdit ? '保存' : '创建',
      busy: _busy,
      child: Form(
        key: _formKey,
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            // 编辑时账号名不可改(标题已含),提供商独占整行;
            // 新建时提供商与账号名并排(CC Switch 式双列)
            if (_isEdit)
              LabeledField(label: '提供商', child: _providerDropdown())
            else
              FormRow2(
                LabeledField(label: '提供商', child: _providerDropdown()),
                LabeledField(
                  label: '账号名',
                  child: TextFormField(
                    key: const ValueKey('account-name'),
                    controller: _name,
                    decoration: const InputDecoration(
                      hintText: 'kimi-1',
                      border: OutlineInputBorder(),
                    ),
                    validator: (v) => (v == null || v.trim().isEmpty)
                        ? '账号名不能为空'
                        : null,
                  ),
                ),
              ),
            const SizedBox(height: 20),
            LabeledField(
              label: '请求地址',
              hint: '默认跟随提供商；修改后仅本账号生效',
              child: TextFormField(
                key: const ValueKey('account-base-url'),
                controller: _baseUrl,
                decoration: const InputDecoration(
                  border: OutlineInputBorder(),
                ),
                validator: (v) {
                  final t = v?.trim() ?? '';
                  if (t.isEmpty) return '请求地址不能为空';
                  if (!t.startsWith('http://') &&
                      !t.startsWith('https://')) {
                    return '需以 http:// 或 https:// 开头';
                  }
                  return null;
                },
              ),
            ),
            const SizedBox(height: 20),
            LabeledField(
              label: '密钥',
              child: TextFormField(
                key: const ValueKey('account-api-key'),
                controller: _apiKey,
                obscureText: !_revealKey,
                decoration: InputDecoration(
                  hintText: _isEdit
                      ? '留空保留原密钥（当前 ${widget.editing!.maskedApiKey}）'
                      : widget.copyFrom != null
                          ? '原密钥不可见（${widget.copyFrom!.maskedApiKey}），需重新填入'
                          : null,
                  border: const OutlineInputBorder(),
                  suffixIcon: IconButton(
                    tooltip: _revealKey ? '隐藏' : '显示',
                    icon: Icon(_revealKey
                        ? Icons.visibility_off_outlined
                        : Icons.visibility_outlined),
                    onPressed: () =>
                        setState(() => _revealKey = !_revealKey),
                  ),
                ),
                validator: (v) {
                  if (_isEdit) return null;
                  return (v == null || v.trim().isEmpty)
                      ? '密钥不能为空'
                      : null;
                },
              ),
            ),
            const SizedBox(height: 26),
            HeaderEditor(
              initial: _headers,
              onChanged: (h) => _headers = h,
            ),
          ],
        ),
      ),
    );
  }
}

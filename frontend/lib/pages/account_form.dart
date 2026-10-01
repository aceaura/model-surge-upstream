import 'package:flutter/material.dart';

import '../api_client.dart';
import '../models.dart';
import '../ui/collapsible_section.dart';
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

  // ── 额度脚本(仿 CC Switch usage_script)──
  late bool _scriptEnabled = _initialScript?.enabled ?? false;
  late final TextEditingController _scriptCode =
      TextEditingController(text: _initialScript?.code ?? '');
  late final TextEditingController _scriptTimeout = TextEditingController(
      text: (_initialScript?.timeoutSeconds ?? 0) > 0
          ? '${_initialScript!.timeoutSeconds}'
          : '');
  late final TextEditingController _scriptInterval = TextEditingController(
      text: (_initialScript?.autoIntervalMinutes ?? 0) > 0
          ? '${_initialScript!.autoIntervalMinutes}'
          : '');
  bool _testingScript = false;
  String? _scriptTestResult;
  bool? _scriptTestOk;

  bool _revealKey = false;
  bool _busy = false;

  bool get _isEdit => widget.editing != null;

  QuotaScript? get _initialScript =>
      widget.editing?.quotaScript ?? widget.copyFrom?.quotaScript;

  @override
  void dispose() {
    _name.dispose();
    _apiKey.dispose();
    _baseUrl.dispose();
    _scriptCode.dispose();
    _scriptTimeout.dispose();
    _scriptInterval.dispose();
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

  /// 额度脚本提交载荷:null=不动服务端配置,空 Map=显式清除。
  /// 停用且代码清空视为"不要脚本";其余状态按表单值全量提交。
  Map<String, dynamic>? _quotaScriptPayload() {
    final code = _scriptCode.text.trim();
    if (!_scriptEnabled && code.isEmpty) {
      return _initialScript == null ? null : <String, dynamic>{};
    }
    return QuotaScript(
      enabled: _scriptEnabled,
      code: code,
      timeoutSeconds: int.tryParse(_scriptTimeout.text.trim()) ?? 0,
      autoIntervalMinutes: int.tryParse(_scriptInterval.text.trim()) ?? 0,
    ).toJson();
  }

  /// 试跑当前编辑器里的脚本(凭据取自已存账号,故仅编辑态可用)。
  Future<void> _testScript() async {
    setState(() {
      _testingScript = true;
      _scriptTestResult = null;
      _scriptTestOk = null;
    });
    try {
      final (ok, error, report) = await widget.client.testQuotaScript(
        widget.editing!.name,
        code: _scriptCode.text.trim(),
        timeoutSeconds: int.tryParse(_scriptTimeout.text.trim()) ?? 0,
      );
      if (!mounted) return;
      setState(() {
        _scriptTestOk = ok;
        if (ok) {
          final meters = report?.meters ?? const <QuotaMeter>[];
          _scriptTestResult = meters.isEmpty
              ? '试跑成功,但未提取到计量项'
              : '试跑成功:${meters.map((m) => m.title).join(' / ')}';
        } else {
          _scriptTestResult = '试跑失败:$error';
        }
      });
    } catch (e) {
      if (!mounted) return;
      setState(() {
        _scriptTestOk = false;
        _scriptTestResult = '试跑请求失败:$e';
      });
    } finally {
      if (mounted) setState(() => _testingScript = false);
    }
  }

  Future<void> _submit() async {
    if (!(_formKey.currentState?.validate() ?? false)) return;
    setState(() => _busy = true);
    try {
      // 与提供商默认一致即视为不覆盖,保持"跟随提供商"语义
      final url = _baseUrl.text.trim();
      final baseUrl = url == (_spec?.baseUrl ?? '') ? '' : url;
      final quotaScript = _quotaScriptPayload();
      if (_isEdit) {
        await widget.client.updateAccount(
          name: widget.editing!.name,
          providerId: _providerId,
          apiKey: _apiKey.text.trim(),
          baseUrl: baseUrl,
          headers: _headers,
          quotaScript: quotaScript,
          enabled: _enabled,
        );
      } else {
        await widget.client.createAccount(
          name: _name.text.trim(),
          providerId: _providerId!,
          apiKey: _apiKey.text.trim(),
          baseUrl: baseUrl,
          headers: _headers,
          quotaScript: quotaScript,
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
            const SizedBox(height: 26),
            _quotaScriptSection(),
          ],
        ),
      ),
    );
  }

  // ── 额度脚本区(仿 CC Switch 脚本弹窗,收进可折叠分栏)──

  /// 预设模板。变量只支持 {{apiKey}}/{{baseUrl}}(后端执行前替换);
  /// CC Switch 的 {{accessToken}}/{{userId}} 在本服务无对应物,New API
  /// 模板已改写为用 apiKey 并去掉 New-Api-User 头。
  static const _scriptTemplates = <String, String>{
    '空白': '''({
  request: {
    url: "",
    method: "GET",
    headers: {}
  },
  extractor: function(response) {
    return {
      remaining: 0,
      unit: "USD"
    };
  }
})''',
    '通用余额': '''({
  request: {
    url: "{{baseUrl}}/user/balance",
    method: "GET",
    headers: {
      "Authorization": "Bearer {{apiKey}}"
    }
  },
  extractor: function(response) {
    return {
      isValid: response.is_active || true,
      remaining: response.balance,
      unit: "USD"
    };
  }
})''',
    'New API': '''({
  request: {
    url: "{{baseUrl}}/api/user/self",
    method: "GET",
    headers: {
      "Content-Type": "application/json",
      "Authorization": "Bearer {{apiKey}}"
    }
  },
  extractor: function(response) {
    if (response.success && response.data) {
      return {
        planName: response.data.group,
        remaining: response.data.quota / 500000,
        used: response.data.used_quota / 500000,
        total: (response.data.quota + response.data.used_quota) / 500000,
        unit: "USD"
      };
    }
    return {
      isValid: false,
      invalidMessage: response.message || "查询失败"
    };
  }
})''',
  };

  String get _scriptSubtitle {
    if (_scriptEnabled && _scriptCode.text.trim().isNotEmpty) {
      return '已启用 · 接管该账号的额度查询';
    }
    if (_scriptCode.text.trim().isNotEmpty) return '已编写未启用';
    return '按渠道定制额度查询代码,未配置时走提供商内置声明';
  }

  Widget _quotaScriptSection() {
    final theme = Theme.of(context);
    return CollapsibleSection(
      icon: Icons.code,
      title: '额度脚本',
      subtitle: _scriptSubtitle,
      initiallyExpanded:
          _scriptEnabled || _scriptCode.text.trim().isNotEmpty,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Row(
            children: [
              Switch(
                key: const ValueKey('quota-script-enabled'),
                value: _scriptEnabled,
                onChanged: (v) => setState(() => _scriptEnabled = v),
              ),
              const SizedBox(width: 8),
              const Expanded(
                child: Text(
                  '启用后由脚本接管该账号的额度查询;停用自动回落提供商内置声明',
                  style: TextStyle(fontSize: 12.5),
                ),
              ),
            ],
          ),
          const SizedBox(height: 14),
          Row(
            children: [
              const Text('脚本代码', style: TextStyle(fontSize: 13)),
              const Spacer(),
              PopupMenuButton<String>(
                key: const ValueKey('quota-script-template'),
                tooltip: '插入预设模板',
                itemBuilder: (context) => [
                  for (final name in _scriptTemplates.keys)
                    PopupMenuItem(value: name, child: Text(name)),
                ],
                onSelected: (name) => setState(() {
                  _scriptCode.text = _scriptTemplates[name]!;
                  _scriptTestResult = null;
                  _scriptTestOk = null;
                }),
                child: const Padding(
                  padding: EdgeInsets.symmetric(horizontal: 8, vertical: 4),
                  child: Row(
                    mainAxisSize: MainAxisSize.min,
                    children: [
                      Icon(Icons.article_outlined, size: 15),
                      SizedBox(width: 4),
                      Text('模板', style: TextStyle(fontSize: 12.5)),
                      Icon(Icons.arrow_drop_down, size: 16),
                    ],
                  ),
                ),
              ),
            ],
          ),
          const SizedBox(height: 6),
          TextFormField(
            key: const ValueKey('quota-script-code'),
            controller: _scriptCode,
            maxLines: 12,
            minLines: 6,
            style: const TextStyle(
              fontSize: 12,
              fontFamily: 'Consolas',
              fontFamilyFallback: ['monospace'],
              height: 1.45,
            ),
            decoration: InputDecoration(
              border: const OutlineInputBorder(),
              hintText: '({ request: {...}, extractor: function(response) {...} })',
              hintStyle: TextStyle(
                fontSize: 12,
                color: theme.hintColor,
                fontFamily: 'Consolas',
                fontFamilyFallback: const ['monospace'],
              ),
            ),
            onChanged: (_) => setState(() {
              _scriptTestResult = null;
              _scriptTestOk = null;
            }),
            validator: (v) => _scriptEnabled && (v == null || v.trim().isEmpty)
                ? '启用脚本时代码不能为空'
                : null,
          ),
          const SizedBox(height: 14),
          FormRow2(
            TextFormField(
              key: const ValueKey('quota-script-timeout'),
              controller: _scriptTimeout,
              keyboardType: TextInputType.number,
              decoration: const InputDecoration(
                labelText: '超时(秒)',
                hintText: '默认 10,上限 120',
                border: OutlineInputBorder(),
              ),
              validator: _intRangeValidator(0, 120),
            ),
            TextFormField(
              key: const ValueKey('quota-script-interval'),
              controller: _scriptInterval,
              keyboardType: TextInputType.number,
              decoration: const InputDecoration(
                labelText: '自动查询间隔(分钟)',
                hintText: '0 表示不自动刷新',
                border: OutlineInputBorder(),
              ),
              validator: _intRangeValidator(0, 1440),
            ),
          ),
          const SizedBox(height: 14),
          Row(
            children: [
              OutlinedButton.icon(
                key: const ValueKey('quota-script-test'),
                onPressed: (_isEdit && !_testingScript) ? _testScript : null,
                icon: _testingScript
                    ? const SizedBox(
                        width: 14,
                        height: 14,
                        child: CircularProgressIndicator(strokeWidth: 2),
                      )
                    : const Icon(Icons.play_arrow_outlined, size: 16),
                label: Text(_testingScript ? '试跑中…' : '试跑脚本'),
              ),
              const SizedBox(width: 10),
              Expanded(
                child: Text(
                  _isEdit ? '用该账号已存凭据试跑,不影响已存配置' : '保存账号后才能试跑',
                  style: TextStyle(fontSize: 12, color: theme.hintColor),
                ),
              ),
            ],
          ),
          if (_scriptTestResult != null) ...[
            const SizedBox(height: 8),
            Text(
              key: const ValueKey('quota-script-test-result'),
              _scriptTestResult!,
              style: TextStyle(
                fontSize: 12,
                color: _scriptTestOk == true
                    ? theme.colorScheme.primary
                    : theme.colorScheme.error,
              ),
            ),
          ],
        ],
      ),
    );
  }

  /// 可留空(走默认)的整数范围校验。
  static String? Function(String?) _intRangeValidator(int min, int max) {
    return (v) {
      final t = v?.trim() ?? '';
      if (t.isEmpty) return null;
      final n = int.tryParse(t);
      if (n == null || n < min || n > max) return '需为 $min-$max 的整数';
      return null;
    };
  }
}

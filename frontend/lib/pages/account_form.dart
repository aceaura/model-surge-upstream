import 'dart:convert';
import 'dart:io';

import 'package:flutter/material.dart';

import '../api_client.dart';
import '../models.dart';
import '../ui/collapsible_section.dart';
import '../ui/feedback.dart';
import '../ui/form_page.dart';
import '../ui/header_editor.dart';
import '../ui/provider_avatar.dart';
import '../ui/styled_dropdown.dart';
import '../ui/top_toast.dart';

/// 本机用户目录(Windows 取 USERPROFILE,其余取 HOME)。
String userHomeDir() =>
    Platform.environment['USERPROFILE'] ?? Platform.environment['HOME'] ?? '';

/// codex CLI 登录态路径(~/.codex/auth.json)。
String codexCliAuthPath() {
  final home = debugCodexHomeOverride ?? userHomeDir();
  return '$home${Platform.pathSeparator}.codex${Platform.pathSeparator}auth.json';
}

/// Codex App 登录态路径:桌面应用的 ChatGPT 订阅登录由 CC Switch 保管。
String codexAppAuthPath() {
  final home = debugCodexHomeOverride ?? userHomeDir();
  return '$home${Platform.pathSeparator}.cc-switch${Platform.pathSeparator}codex_oauth_auth.json';
}

/// codex CLI auth.json:取 tokens.refresh_token / tokens.account_id;
/// 只有 OPENAI_API_KEY 说明是 API Key 登录,不是订阅登录。
(String, String) parseCodexCliAuth(String text) {
  final decoded = jsonDecode(text);
  final tokens = decoded is Map ? decoded['tokens'] : null;
  final rt = tokens is Map ? tokens['refresh_token'] : null;
  final id = tokens is Map ? tokens['account_id'] : null;
  if (rt is String && rt.isNotEmpty && id is String && id.isNotEmpty) {
    return (rt, id);
  }
  throw const FormatException('auth.json 里没有订阅登录态(可能是 API Key 登录)');
}

/// Codex App 登录态(CC Switch 保管库):取默认账号的 refresh_token 与
/// chatgpt_account_id(accounts 的 map key 是 CC Switch 内部 id,不能当
/// ChatGPT account_id 用)。
(String, String) parseCodexAppAuth(String text) {
  final decoded = jsonDecode(text);
  final accounts = decoded is Map ? decoded['accounts'] : null;
  final acc = accounts is Map ? accounts[decoded['default_account_id']] : null;
  final rt = acc is Map ? acc['refresh_token'] : null;
  final id = acc is Map ? acc['chatgpt_account_id'] : null;
  if (rt is String && rt.isNotEmpty && id is String && id.isNotEmpty) {
    return (rt, id);
  }
  throw const FormatException('登录态里缺少 refresh_token 或 chatgpt_account_id');
}

/// 测试覆写:指向临时用户目录,避免碰真实用户目录。
@visibleForTesting
String? debugCodexHomeOverride;

/// kimi-desktop 本地存储目录(leveldb 里存着网页会话的 refresh/access token)。
String kimiDesktopLeveldbDir() {
  final override = debugKimiDesktopDirOverride;
  if (override != null) return override;
  final appData = Platform.environment['APPDATA'] ?? '';
  return '$appData${Platform.pathSeparator}kimi-desktop'
      '${Platform.pathSeparator}Local Storage${Platform.pathSeparator}leveldb';
}

/// 测试覆写:指向临时 leveldb 目录,避免碰真实 kimi-desktop 数据。
@visibleForTesting
String? debugKimiDesktopDirOverride;

final _jwtPattern =
    RegExp(r'eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+');

/// 从 kimi-desktop leveldb 文件字节里挑网页会话 refresh_token:JWT 三段、
/// 载荷 iss=user-center 且 typ=refresh,取 exp 最大者(access token 短效
/// 且 type 不匹配,刷新链只认 refresh)。找不到即抛 FormatException,
/// 由调用方报「未找到登录态」。
String parseKimiWebRefreshToken(Iterable<List<int>> blobs) {
  String? best;
  var bestExp = 0;
  for (final blob in blobs) {
    final text = utf8.decode(blob, allowMalformed: true);
    for (final m in _jwtPattern.allMatches(text)) {
      final token = m.group(0)!;
      final parts = token.split('.');
      if (parts.length != 3) continue;
      Map<String, dynamic> payload;
      try {
        final decoded = jsonDecode(
            utf8.decode(base64Url.decode(base64Url.normalize(parts[1]))));
        if (decoded is! Map<String, dynamic>) continue;
        payload = decoded;
      } on FormatException {
        continue;
      }
      if (payload['iss'] != 'user-center' || payload['typ'] != 'refresh') {
        continue;
      }
      final exp = (payload['exp'] as num?)?.toInt() ?? 0;
      if (exp > bestExp) {
        best = token;
        bestExp = exp;
      }
    }
  }
  if (best == null) {
    throw const FormatException('本地存储里没有网页会话登录态(请先登录 kimi-desktop)');
  }
  return best;
}

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
  // oauth_refresh(订阅登录态)字段:refresh_token 永不下发明文,
  // 编辑态留空表示保留;account_id 是标识不是秘密,预填回显。
  late final TextEditingController _refreshToken = TextEditingController();
  late final TextEditingController _accountId = TextEditingController(
      text: widget.editing?.accountId ?? widget.copyFrom?.accountId ?? '');
  // kimi 网页会话 token(api_key 形态的可选附加凭据):会员月总额度只在
  // 网页网关可查,API key 拿不到;永不下发明文,编辑态留空表示保留。
  late final TextEditingController _webRefreshToken = TextEditingController();

  late String? _providerId =
      widget.editing?.providerId ?? widget.copyFrom?.providerId;
  // 三级级联选择(厂商→计费模式→服务区域):编辑/拷贝时按已存 providerId 反推
  late String? _vendor = _spec?.displayName;
  late String? _billing = _spec?.billingLabel;
  late String? _region = _spec?.regionLabel;

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
  late final TextEditingController _scriptStopInterval = TextEditingController(
      text: _initialStopInterval());
  bool _testingScript = false;
  String? _scriptTestResult;
  bool? _scriptTestOk;

  /// 停止查询间隔初值:已有配置(编辑/拷贝)按存储值回显,0=留空走后端
  /// 默认;全新表单一上来就填 5(与后端默认一致,让用户看见默认值)。
  String _initialStopInterval() {
    final v = _initialScript?.stopIntervalMinutes ?? 0;
    if (v > 0) return '$v';
    return (widget.editing == null && widget.copyFrom == null) ? '5' : '';
  }

  /// 脚本自定义变量行,按名排序回显,保证与后端 map 迭代顺序无关。
  late final List<_ScriptVar> _scriptVars = _initialScriptVars();

  List<_ScriptVar> _initialScriptVars() {
    final vars = _initialScript?.variables ?? const <String, String>{};
    final names = vars.keys.toList()..sort();
    return [for (final n in names) _ScriptVar(n, vars[n]!)];
  }

  /// 当前变量行的提交形态:空名行丢弃,名去首尾空白。
  Map<String, String> _scriptVariables() {
    final out = <String, String>{};
    for (final v in _scriptVars) {
      final k = v.name.text.trim();
      if (k.isNotEmpty) out[k] = v.value.text;
    }
    return out;
  }

  bool _revealKey = false;
  bool _busy = false;

  bool get _isEdit => widget.editing != null;

  QuotaScript? get _initialScript =>
      widget.editing?.quotaScript ?? widget.copyFrom?.quotaScript;

  @override
  void dispose() {
    _name.dispose();
    _apiKey.dispose();
    _refreshToken.dispose();
    _accountId.dispose();
    _webRefreshToken.dispose();
    _baseUrl.dispose();
    _scriptCode.dispose();
    _scriptTimeout.dispose();
    _scriptInterval.dispose();
    _scriptStopInterval.dispose();
    for (final v in _scriptVars) {
      v.name.dispose();
      v.value.dispose();
    }
    super.dispose();
  }

  /// 当前选中的提供商是否 OAuth 登录态(订阅)凭据形态。
  bool get _isOAuth => _spec?.credential == 'oauth_refresh';

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
    // 新建:三级级联选定前无提供商,地址留空,选定后自动带默认值
    return '';
  }

  /// 三级级联任一级变化后重解析 provider:地址为空或仍是旧默认值时
  /// 跟随新默认值,用户改过则保留其输入。
  void _resolveProvider() {
    final match = widget.providers
        .where((p) =>
            p.displayName == _vendor &&
            p.billingLabel == _billing &&
            p.regionLabel == _region)
        .firstOrNull;
    final oldDefault = _defaultBaseUrlFor(_providerId);
    final cur = _baseUrl.text.trim();
    if (cur.isEmpty || (oldDefault.isNotEmpty && cur == oldDefault)) {
      _baseUrl.text = match?.baseUrl ?? '';
    }
    _providerId = match?.id;
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
      stopIntervalMinutes: int.tryParse(_scriptStopInterval.text.trim()) ?? 0,
      variables: _scriptVariables(),
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
        variables: _scriptVariables(),
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

  /// oauth 凭据提交载荷:null=不是 oauth 形态或编辑态保留原登录态。
  /// 服务端要求 oauth_refresh 必带 refresh_token,所以编辑态只有用户
  /// 重新粘贴了 refresh_token 才整体替换凭据。
  /// kimi(api_key 形态)走完整 credential 对象以携带网页会话 token;
  /// 服务端按字段合并,留空的密钥/网页 token 都保留原值。
  Map<String, dynamic>? _credentialPayload() {
    if (_providerId == 'kimi') {
      return {
        'kind': 'api_key',
        'api_key': _apiKey.text.trim(),
        'web_refresh_token': _webRefreshToken.text.trim(),
      };
    }
    if (!_isOAuth) return null;
    final rt = _refreshToken.text.trim();
    if (_isEdit && rt.isEmpty) return null;
    return {
      'kind': 'oauth_refresh',
      'refresh_token': rt,
      'account_id': _accountId.text.trim(),
    };
  }

  Future<void> _submit() async {
    if (!(_formKey.currentState?.validate() ?? false)) return;
    setState(() => _busy = true);
    try {
      // 与提供商默认一致即视为不覆盖,保持"跟随提供商"语义
      final url = _baseUrl.text.trim();
      final baseUrl = url == (_spec?.baseUrl ?? '') ? '' : url;
      final quotaScript = _quotaScriptPayload();
      final credential = _credentialPayload();
      if (_isEdit) {
        await widget.client.updateAccount(
          name: widget.editing!.name,
          providerId: _providerId,
          apiKey: _apiKey.text.trim(),
          baseUrl: baseUrl,
          headers: _headers,
          quotaScript: quotaScript,
          enabled: _enabled,
          credential: credential,
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
          credential: credential,
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

  // ── 提供商三级级联:厂商→计费模式→服务区域,选项由清单动态推导,
  // 上级未选下级禁用,换上级重置下级 ──

  List<String> get _vendorOptions =>
      {for (final p in widget.providers) p.displayName}.toList();

  List<String> get _billingOptions => {
        for (final p in widget.providers)
          if (p.displayName == _vendor) p.billingLabel
      }.toList();

  List<String> get _regionOptions => {
        for (final p in widget.providers)
          if (p.displayName == _vendor && p.billingLabel == _billing)
            p.regionLabel
      }.toList();

  Widget _vendorDropdown() => StyledDropdownFormField(
        key: const ValueKey('account-provider-vendor'),
        value: _vendor,
        decoration: const InputDecoration(border: OutlineInputBorder()),
        options: _vendorOptions,
        onChanged: (v) => setState(() {
          _vendor = v;
          _billing = null;
          _region = null;
          _resolveProvider();
        }),
        validator: (v) => v == null ? '请选择提供商' : null,
      );

  Widget _billingDropdown() => StyledDropdownFormField(
        key: const ValueKey('account-provider-billing'),
        value: _billing,
        enabled: _vendor != null,
        decoration: const InputDecoration(border: OutlineInputBorder()),
        options: _billingOptions,
        onChanged: (v) => setState(() {
          _billing = v;
          _region = null;
          _resolveProvider();
        }),
        validator: (v) => v == null ? '请选择计费模式' : null,
      );

  Widget _regionDropdown() => StyledDropdownFormField(
        key: const ValueKey('account-provider-region'),
        value: _region,
        enabled: _billing != null,
        decoration: const InputDecoration(border: OutlineInputBorder()),
        options: _regionOptions,
        onChanged: (v) => setState(() {
          _region = v;
          _resolveProvider();
        }),
        validator: (v) => v == null ? '请选择服务区域' : null,
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
            _basicSection(),
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

  // ── 基本信息(提供商/账号名/请求地址/密钥),收进可折叠分栏 ──

  String get _basicSubtitle {
    final p =
        widget.providers.where((p) => p.id == _providerId).firstOrNull;
    final name = p?.displayName ?? _providerId ?? '';
    return name.isEmpty ? '提供商、请求地址与密钥' : name;
  }

  Widget _basicSection() {
    return CollapsibleSection(
      icon: Icons.badge_outlined,
      title: '基本信息',
      subtitle: _basicSubtitle,
      // 主信息默认展开,收起时靠副标题辨认当前配置。
      initiallyExpanded: true,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          // 账号名/厂商/计费模式/服务区域四个配置一行一个,账号名居首;
          // 编辑态账号名是资源键不可改,灰框禁用展示;
          // 计费模式/服务区域是级联下两级,从上至下依次解锁
          LabeledField(
            label: '账号名',
            child: TextFormField(
              key: const ValueKey('account-name'),
              controller: _name,
              enabled: !_isEdit,
              decoration: const InputDecoration(
                border: OutlineInputBorder(),
              ),
              validator: (v) => (v == null || v.trim().isEmpty)
                  ? '账号名不能为空'
                  : null,
            ),
          ),
          const SizedBox(height: 20),
          LabeledField(label: '提供商', child: _vendorDropdown()),
          const SizedBox(height: 20),
          LabeledField(label: '计费模式', child: _billingDropdown()),
          const SizedBox(height: 20),
          LabeledField(label: '服务区域', child: _regionDropdown()),
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
                if (!t.startsWith('http://') && !t.startsWith('https://')) {
                  return '需以 http:// 或 https:// 开头';
                }
                return null;
              },
            ),
          ),
          const SizedBox(height: 20),
          if (_isOAuth && _isEdit && widget.editing!.needsReauth) ...[
            Container(
              key: const ValueKey('oauth-reauth-banner'),
              padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 10),
              decoration: BoxDecoration(
                color: Theme.of(context).colorScheme.errorContainer,
                borderRadius: BorderRadius.circular(8),
              ),
              child: Text(
                '登录态已失效,请重新粘贴 Refresh Token 后保存',
                style: TextStyle(
                  fontSize: 12.5,
                  color: Theme.of(context).colorScheme.onErrorContainer,
                ),
              ),
            ),
            const SizedBox(height: 20),
          ],
          if (_isOAuth) ..._oauthFields() else _apiKeyField(),
          if (_providerId == 'kimi') ...[
            const SizedBox(height: 20),
            _kimiWebTokenField(),
          ],
        ],
      ),
    );
  }

  // ── 凭据区:api_key 单框 / oauth_refresh 双框,按提供商声明的形态分流 ──

  Widget _apiKeyField() {
    return LabeledField(
      label: '密钥',
      child: TextFormField(
        key: const ValueKey('account-api-key'),
        controller: _apiKey,
        obscureText: !_revealKey,
        decoration: InputDecoration(
          // 默认纯星号不泄露任何字符,点眼睛才亮部分
          // 掩码帮助辨认;完整密钥后端从不下发。
          hintText: _isEdit
              ? _revealKey
                  ? widget.editing!.maskedApiKey
                  : '************'
              : widget.copyFrom != null
                  ? _revealKey
                      ? widget.copyFrom!.maskedApiKey
                      : '************'
                  : null,
          border: const OutlineInputBorder(),
          suffixIcon: IconButton(
            tooltip: _revealKey ? '隐藏' : '显示',
            icon: Icon(_revealKey
                ? Icons.visibility_off_outlined
                : Icons.visibility_outlined),
            onPressed: () => setState(() => _revealKey = !_revealKey),
          ),
        ),
        validator: (v) {
          if (_isEdit) return null;
          return (v == null || v.trim().isEmpty) ? '密钥不能为空' : null;
        },
      ),
    );
  }

  /// kimi 网页会话 token:月度会员额度查询链的凭据。与密钥同款交互——
  /// 默认纯星号、眼睛亮掩码、完整 token 后端从不下发;可选手动粘贴或
  /// 从本机 kimi-desktop 本地存储自动提取。
  Widget _kimiWebTokenField() {
    final masked = widget.editing?.maskedWebRefreshToken ??
        widget.copyFrom?.maskedWebRefreshToken ??
        '';
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        LabeledField(
          label: '网页会话 Token',
          hint: '可选;填入后额度页可显示会员月总额度。'
              '${_isEdit ? '留空保留原值' : ''}',
          child: TextFormField(
            key: const ValueKey('account-web-refresh-token'),
            controller: _webRefreshToken,
            obscureText: !_revealKey,
            decoration: InputDecoration(
              hintText: (_isEdit || widget.copyFrom != null)
                  ? _revealKey
                      ? masked
                      : '************'
                  : null,
              border: const OutlineInputBorder(),
              suffixIcon: IconButton(
                tooltip: _revealKey ? '隐藏' : '显示',
                icon: Icon(_revealKey
                    ? Icons.visibility_off_outlined
                    : Icons.visibility_outlined),
                onPressed: () => setState(() => _revealKey = !_revealKey),
              ),
            ),
          ),
        ),
        const SizedBox(height: 12),
        OutlinedButton.icon(
          key: const ValueKey('kimi-autofill-desktop'),
          icon: const Icon(Icons.desktop_windows_outlined, size: 18),
          label: const Text('从 kimi-desktop 获取'),
          onPressed: _fillKimiWebToken,
        ),
      ],
    );
  }

  /// 扫 kimi-desktop leveldb 的 .log/.ldb 文件,挑出网页会话 refresh_token
  /// 填入。本地小文件,同步读避免异步缝隙里表单已销毁。
  void _fillKimiWebToken() {
    try {
      final dir = Directory(kimiDesktopLeveldbDir());
      if (!dir.existsSync()) {
        throw const FormatException('未找到 kimi-desktop 本地存储(请先安装并登录)');
      }
      final blobs = dir
          .listSync()
          .whereType<File>()
          .where((f) => f.path.endsWith('.log') || f.path.endsWith('.ldb'))
          .map((f) => f.readAsBytesSync());
      final token = parseKimiWebRefreshToken(blobs);
      setState(() => _webRefreshToken.text = token);
      TopToast.show(context, '已填入 kimi-desktop 的网页会话登录态');
    } on FormatException catch (e) {
      TopToast.show(context, 'kimi-desktop: ${e.message}', error: true);
    }
  }

  List<Widget> _oauthFields() {
    final masked = widget.editing?.maskedRefreshToken ??
        widget.copyFrom?.maskedRefreshToken ??
        '';
    return [
      LabeledField(
        label: 'Refresh Token',
        hint: '可用下方按钮从 codex CLI / Codex App 自动获取;'
            '${_isEdit ? '留空保留原登录态' : '粘贴后由服务端自动续期'}',
        child: TextFormField(
          key: const ValueKey('account-refresh-token'),
          controller: _refreshToken,
          obscureText: !_revealKey,
          decoration: InputDecoration(
            // 与密钥同款:默认纯星号,眼睛才亮掩码;完整 token 后端从不下发。
            hintText: (_isEdit || widget.copyFrom != null)
                ? _revealKey
                    ? masked
                    : '************'
                : null,
            border: const OutlineInputBorder(),
            suffixIcon: IconButton(
              tooltip: _revealKey ? '隐藏' : '显示',
              icon: Icon(_revealKey
                  ? Icons.visibility_off_outlined
                  : Icons.visibility_outlined),
              onPressed: () => setState(() => _revealKey = !_revealKey),
            ),
          ),
          validator: (v) {
            if (_isEdit) return null;
            return (v == null || v.trim().isEmpty)
                ? 'Refresh Token 不能为空'
                : null;
          },
        ),
      ),
      const SizedBox(height: 20),
      LabeledField(
        label: 'Account ID',
        hint: '登录态里的 ChatGPT account_id',
        child: TextFormField(
          key: const ValueKey('account-account-id'),
          controller: _accountId,
          decoration: const InputDecoration(
            border: OutlineInputBorder(),
          ),
          validator: (v) => (v == null || v.trim().isEmpty)
              ? 'Account ID 不能为空'
              : null,
        ),
      ),
      const SizedBox(height: 12),
      Row(
        children: [
          OutlinedButton.icon(
            key: const ValueKey('oauth-autofill-cli'),
            icon: const Icon(Icons.terminal_outlined, size: 18),
            label: const Text('从 codex CLI 获取'),
            onPressed: () =>
                _fillFrom('codex CLI', codexCliAuthPath(), parseCodexCliAuth),
          ),
          const SizedBox(width: 10),
          OutlinedButton.icon(
            key: const ValueKey('oauth-autofill-app'),
            icon: const Icon(Icons.bolt_outlined, size: 18),
            label: const Text('从 Codex App 获取'),
            onPressed: () =>
                _fillFrom('Codex App', codexAppAuthPath(), parseCodexAppAuth),
          ),
        ],
      ),
    ];
  }

  /// 从指定来源读登录态填入 refresh_token 与 account_id;缺失/格式错误
  /// 只报该来源。本地几 KB 小文件,同步读避免异步缝隙里表单已销毁。
  void _fillFrom(
      String label, String path, (String, String) Function(String) parse) {
    try {
      final (rt, id) = parse(File(path).readAsStringSync());
      setState(() {
        _refreshToken.text = rt;
        _accountId.text = id;
      });
      TopToast.show(context, '已填入 $label 的登录态');
    } on FileSystemException {
      TopToast.show(context, '未找到 $label 的登录态($path)', error: true);
    } on FormatException catch (e) {
      TopToast.show(context, '$label: ${e.message}', error: true);
    }
  }

  // ── 额度脚本区(仿 CC Switch 脚本弹窗,收进可折叠分栏)──

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
                  '启用脚本',
                  style: TextStyle(fontSize: 12.5),
                ),
              ),
            ],
          ),
          const SizedBox(height: 14),
          const Align(
            alignment: Alignment.centerLeft,
            child: Text('变量', style: TextStyle(fontSize: 13)),
          ),
          const SizedBox(height: 6),
          // 内置两个变量:值由凭据与生效地址在执行时注入,只读展示;
          // 自定义行仿请求头的键值动态行,脚本里以 {{名}} 引用。
          for (final b in const [
            ('apiKey', '账号密钥 · 执行时自动注入'),
            ('baseUrl', '请求地址 · 执行时自动注入'),
          ])
            Padding(
              padding: const EdgeInsets.only(bottom: 8),
              child: Row(
                children: [
                  Expanded(
                    child: Text(b.$1,
                        style: const TextStyle(
                            fontSize: 12.5,
                            fontFamily: 'Consolas',
                            fontFamilyFallback: ['monospace'])),
                  ),
                  const SizedBox(width: 8),
                  Expanded(
                    flex: 2,
                    child: Text(b.$2,
                        style: TextStyle(fontSize: 12, color: theme.hintColor)),
                  ),
                  const SizedBox(width: 40),
                ],
              ),
            ),
          for (var i = 0; i < _scriptVars.length; i++)
            Padding(
              padding: const EdgeInsets.only(bottom: 8),
              child: Row(
                children: [
                  Expanded(
                    child: TextFormField(
                      key: ValueKey('script-var-name-$i'),
                      controller: _scriptVars[i].name,
                      decoration: const InputDecoration(
                        hintText: '名',
                      ),
                    ),
                  ),
                  const SizedBox(width: 8),
                  Expanded(
                    flex: 2,
                    child: TextFormField(
                      key: ValueKey('script-var-value-$i'),
                      controller: _scriptVars[i].value,
                      decoration: const InputDecoration(
                        hintText: '值',
                      ),
                    ),
                  ),
                  IconButton(
                    key: ValueKey('script-var-del-$i'),
                    tooltip: '删除',
                    icon: const Icon(Icons.remove_circle_outline),
                    onPressed: () => setState(() {
                      final v = _scriptVars.removeAt(i);
                      v.name.dispose();
                      v.value.dispose();
                    }),
                  ),
                ],
              ),
            ),
          Align(
            alignment: Alignment.centerLeft,
            child: TextButton.icon(
              key: const ValueKey('script-var-add'),
              onPressed: () => setState(() => _scriptVars.add(_ScriptVar())),
              icon: const Icon(Icons.add, size: 16),
              label: const Text('添加变量'),
            ),
          ),
          const SizedBox(height: 6),
          const Align(
            alignment: Alignment.centerLeft,
            child: Text('脚本代码', style: TextStyle(fontSize: 13)),
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
          Row(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Expanded(
                child: TextFormField(
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
              ),
              const SizedBox(width: 18),
              Expanded(
                child: TextFormField(
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
              const SizedBox(width: 18),
              Expanded(
                child: TextFormField(
                  key: const ValueKey('quota-script-stop-interval'),
                  controller: _scriptStopInterval,
                  keyboardType: TextInputType.number,
                  decoration: const InputDecoration(
                    labelText: '不活跃停止查询间隔(分钟)',
                    hintText: '默认5分钟',
                    border: OutlineInputBorder(),
                  ),
                  validator: _intRangeValidator(0, 1440),
                ),
              ),
            ],
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

/// 一行脚本自定义变量,名与值各持一个控制器(动态增删行需要稳定的
/// 编辑态,不能用 initialValue 靠位置复用)。
class _ScriptVar {
  _ScriptVar([String name = '', String value = ''])
      : name = TextEditingController(text: name),
        value = TextEditingController(text: value);

  final TextEditingController name;
  final TextEditingController value;
}

import 'dart:convert';
import 'dart:io';

import 'package:flutter/material.dart';

import '../api_client.dart';
import '../models.dart';
import '../theme.dart';
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

/// Kiro 只读导入固定缓存文件;测试必须覆写到临时 home。
@visibleForTesting
String? debugKiroHomeOverride;

String kiroCacheDir() {
  final home = debugKiroHomeOverride ?? userHomeDir();
  if (home.isEmpty) throw const FormatException('无法定位用户目录');
  return [home, '.aws', 'sso', 'cache'].join(Platform.pathSeparator);
}

Map<String, dynamic> _kiroJson(String text) {
  try {
    final decoded = jsonDecode(text);
    if (decoded is Map<String, dynamic>) return decoded;
  } on FormatException {
    // JSON 解码异常可能含原文,绝不回显。
  }
  throw const FormatException('Kiro credential JSON 格式无效');
}

/// Desktop / SSO camelCase 缓存 → 后端 credential;不做本机文件探测。
Map<String, String> parseKiroCredentialJson(String text) {
  final data = _kiroJson(text);
  String field(String name) {
    final value = data[name];
    if (value == null) return '';
    if (value is! String) {
      throw const FormatException('Kiro credential 字段需为字符串');
    }
    return value.trim();
  }

  final out = <String, String>{'kind': 'kiro_refresh'};
  for (final entry in const {
    'refreshToken': 'refresh_token',
    'profileArn': 'profile_arn',
    'region': 'region',
    'apiRegion': 'api_region',
    'clientId': 'client_id',
    'clientSecret': 'client_secret',
    'accessToken': 'access_token',
    'expiresAt': 'expiry',
  }.entries) {
    out[entry.value] = field(entry.key);
  }
  for (final name in ['refresh_token', 'client_secret', 'access_token']) {
    final value = out[name]!;
    if (value.isNotEmpty &&
        (RegExp(r'[\s\x00-\x1f\x7f]').hasMatch(value) ||
            RegExp(r'^\*+$').hasMatch(value))) {
      throw const FormatException('Kiro credential 含无效的敏感字段');
    }
  }
  if (out['refresh_token']!.isEmpty) {
    throw const FormatException('Kiro credential 缺少 Refresh Token');
  }
  if (out['client_id']!.isEmpty != out['client_secret']!.isEmpty) {
    throw const FormatException('SSO Client ID 与 Client Secret 必须一起填写');
  }
  if (out['region']!.isEmpty) out['region'] = 'us-east-1';
  final expiry = out['expiry']!;
  if (expiry.isNotEmpty) {
    final date = DateTime.tryParse(expiry);
    if (date == null) {
      throw const FormatException('Kiro credential 到期时间无效');
    }
    out['expiry'] = date.toUtc().toIso8601String();
  }
  return out;
}

/// 不枚举目录,不接受外部路径;拒绝路径穿越及逃逸缓存目录的符号链接。
Map<String, dynamic> _readKiroCacheDocument(String filename) {
  final root = Directory(kiroCacheDir()).resolveSymbolicLinksSync();
  final file = File('$root${Platform.pathSeparator}$filename');
  final resolved = file.resolveSymbolicLinksSync();
  final parent = File(resolved).parent.path;
  if ((Platform.isWindows ? parent.toLowerCase() : parent) !=
      (Platform.isWindows ? root.toLowerCase() : root)) {
    throw const FormatException('Kiro 缓存文件必须位于缓存目录内');
  }
  return _kiroJson(File(resolved).readAsStringSync());
}

/// 手动 JSON 与 App 导入共用 SSO hash 匹配,仅在存在 hash 时读注册文件。
Map<String, String> importKiroCredentialJson(String text) {
  final data = _kiroJson(text);
  final hash = data['clientIdHash'];
  if (hash != null) {
    if (hash is! String || !RegExp(r'^[a-zA-Z0-9_-]{1,128}$').hasMatch(hash)) {
      throw const FormatException('Kiro SSO 注册文件标识无效');
    }
    final registration = _readKiroCacheDocument('$hash.json');
    // 与 Kiro App 一致:token 文件的直接字段优先于注册文件。
    for (final name in ['clientId', 'clientSecret']) {
      if (!data.containsKey(name)) data[name] = registration[name];
    }
    if (data['clientId'] == null || data['clientSecret'] == null) {
      throw const FormatException('Kiro SSO 注册文件缺少客户端凭据');
    }
  }
  return parseKiroCredentialJson(jsonEncode(data));
}

/// Kiro App 当前登录 profile 的存放目录(Windows %APPDATA%\Kiro,macOS
/// ~/Library/Application Support/Kiro,其余 ~/.config/Kiro);测试随
/// debugKiroHomeOverride 落到临时 home,不碰真实应用数据。
String kiroGlobalStorageDir() {
  const tail = ['Kiro', 'User', 'globalStorage', 'kiro.kiroagent'];
  final override = debugKiroHomeOverride;
  final sep = Platform.pathSeparator;
  if (override != null) return [override, ...tail].join(sep);
  if (Platform.isWindows) {
    final appData = Platform.environment['APPDATA'] ?? '';
    return [appData, ...tail].join(sep);
  }
  final home = userHomeDir();
  if (Platform.isMacOS) {
    return [home, 'Library', 'Application Support', ...tail].join(sep);
  }
  return [home, '.config', ...tail].join(sep);
}

/// IdC/IAM 账号的 kiro-auth-token.json 不含 profileArn(App 把它存在
/// globalStorage/profile.json),且其续期走 AWS OIDC 不回传 arn,导入时
/// 必须从这里补,否则额度查询恒缺 profileArn。
Map<String, String> loadKiroAppCredentials() {
  final credential = importKiroCredentialJson(
    jsonEncode(_readKiroCacheDocument('kiro-auth-token.json')),
  );
  if (credential['profile_arn']!.isEmpty) {
    final profile = File(
      [kiroGlobalStorageDir(), 'profile.json'].join(Platform.pathSeparator),
    );
    if (profile.existsSync()) {
      String? arn;
      try {
        final value = _kiroJson(profile.readAsStringSync())['arn'];
        if (value is String) arn = value.trim();
      } on FormatException {
        // profile.json 只是补充来源,损坏不阻断 token 导入。
      }
      if (arn != null && arn.isNotEmpty) {
        credential['profile_arn'] = arn;
      }
    }
  }
  return credential;
}

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

final _jwtPattern = RegExp(
  r'eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+',
);

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
          utf8.decode(base64Url.decode(base64Url.normalize(parts[1]))),
        );
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
  late Account? _editing = widget.editing;
  bool _savedInPlace = false;
  final _formKey = GlobalKey<FormState>();
  late final TextEditingController _name = TextEditingController(
    text:
        _editing?.name ??
        (widget.copyFrom != null ? '${widget.copyFrom!.name}-copy' : ''),
  );
  late final TextEditingController _apiKey = TextEditingController();
  // oauth_refresh(订阅登录态)字段:refresh_token 永不下发明文,
  // 编辑态留空表示保留;account_id 是标识不是秘密,预填回显。
  late final TextEditingController _refreshToken = TextEditingController();
  late final TextEditingController _accountId = TextEditingController(
    text: _editing?.accountId ?? widget.copyFrom?.accountId ?? '',
  );
  // kimi 网页会话 token(api_key 形态的可选附加凭据):会员月总额度只在
  // 网页网关可查,API key 拿不到;永不下发明文,编辑态留空表示保留。
  late final TextEditingController _webRefreshToken = TextEditingController();
  // 百炼 AK/SK 随账号保存;输入框只装新值,绝不装入服务端掩码。
  late final TextEditingController _bailianAkId = TextEditingController();
  late final TextEditingController _bailianAkSecret = TextEditingController();
  late final TextEditingController _profileArn = TextEditingController(
    text: _editing?.profileArn ?? widget.copyFrom?.profileArn ?? '',
  );
  late final TextEditingController _authRegion = TextEditingController(
    text: _editing?.region ?? widget.copyFrom?.region ?? 'us-east-1',
  );
  late final TextEditingController _apiRegion = TextEditingController(
    text: _editing?.apiRegion ?? widget.copyFrom?.apiRegion ?? '',
  );
  late final TextEditingController _clientId = TextEditingController(
    text: _editing?.clientId ?? widget.copyFrom?.clientId ?? '',
  );
  final _clientSecret = TextEditingController();
  final _revealedKiroFields = <String>{};
  String _kiroAccessToken = '';
  String _kiroExpiry = '';

  late String? _providerId =
      _editing?.providerId ?? widget.copyFrom?.providerId;
  // 编辑/拷贝时按已存 providerId 反推级联选项。
  late String? _vendor = _spec?.displayName;
  late String? _billing = _spec?.billingLabel;
  late String? _region = _spec?.regionLabel;
  late String? _plan = _spec != null && _spec!.plan.isNotEmpty
      ? _spec!.plan
      : null;

  // 请求地址可编辑:默认取提供商默认地址,已存覆盖值时取覆盖值
  late final TextEditingController _baseUrl = TextEditingController(
    text: _initialBaseUrl(),
  );
  late Map<String, String> _headers = {
    ...?_editing?.headers ?? widget.copyFrom?.headers,
  };
  // 启停由列表行的开关控制,表单不再展示;编辑/拷贝时沿用原值提交,
  // 新建默认启用
  late final bool _enabled =
      _editing?.enabled ?? widget.copyFrom?.enabled ?? true;

  // ── 额度查询(走供应商内置实现,这里配总开关与两个调度间隔)──
  late bool _quotaEnabled = _initialSettings?.quotaEnabled ?? true;
  late final TextEditingController _quotaInterval = TextEditingController(
    text: _initialInterval(_initialSettings?.autoIntervalMinutes ?? 0),
  );
  late final TextEditingController _quotaStopInterval = TextEditingController(
    text: _initialInterval(_initialSettings?.stopIntervalMinutes ?? 0),
  );

  bool _revealKey = false;
  bool _revealBailianAkId = false;
  bool _revealBailianAkSecret = false;
  bool _busy = false;

  bool get _isEdit => _editing != null;

  QuotaSettings? get _initialSettings =>
      _editing?.quotaSettings ?? widget.copyFrom?.quotaSettings;

  @override
  void dispose() {
    _name.dispose();
    _apiKey.dispose();
    _refreshToken.dispose();
    _accountId.dispose();
    _webRefreshToken.dispose();
    _bailianAkId.dispose();
    _bailianAkSecret.dispose();
    _profileArn.dispose();
    _authRegion.dispose();
    _apiRegion.dispose();
    _clientId.dispose();
    _clientSecret.dispose();
    _baseUrl.dispose();
    _quotaInterval.dispose();
    _quotaStopInterval.dispose();
    super.dispose();
  }

  /// 调度间隔初值:已有配置(编辑/拷贝)按存储值回显,0=留空走后端
  /// 默认;全新表单两个间隔一上来都填 5(与后端默认一致,让用户看见默认值)。
  String _initialInterval(int stored) {
    if (stored > 0) return '$stored';
    return (_editing == null && widget.copyFrom == null) ? '5' : '';
  }

  /// 当前选中的提供商是否 OAuth 登录态(订阅)凭据形态。
  bool get _isOAuth => _spec?.credential == 'oauth_refresh';
  bool get _isKiro => _spec?.credential == 'kiro_refresh';
  bool get _hasStoredKiroCredentials =>
      _editing?.credentialKind == 'kiro_refresh' &&
      _editing?.providerId == _providerId;

  ProviderSpec? get _spec =>
      widget.providers.where((p) => p.id == _providerId).firstOrNull;

  String _defaultBaseUrlFor(String? providerId) =>
      widget.providers.where((p) => p.id == providerId).firstOrNull?.baseUrl ??
      '';

  String _initialBaseUrl() {
    final source = _editing ?? widget.copyFrom;
    if (source != null) {
      return source.baseUrl.isNotEmpty
          ? source.baseUrl
          : _defaultBaseUrlFor(source.providerId);
    }
    // 新建:级联选定前无提供商,地址留空,选定后自动带默认值
    return '';
  }

  void _resolveProvider() {
    final plans = _planOptions;
    final effectivePlan = plans.length > 1
        ? _plan
        : (plans.isEmpty ? null : plans.first);
    final match = widget.providers
        .where(
          (p) =>
              p.displayName == _vendor &&
              p.billingLabel == _billing &&
              p.regionLabel == _region &&
              p.plan == effectivePlan,
        )
        .firstOrNull;
    final oldDefault = _defaultBaseUrlFor(_providerId);
    final cur = _baseUrl.text.trim();
    if (cur.isEmpty || (oldDefault.isNotEmpty && cur == oldDefault)) {
      _baseUrl.text = match?.baseUrl ?? '';
    }
    _providerId = match?.id;
  }

  /// 额度查询提交载荷:null=不动服务端配置,空 Map=显式清除。
  /// 开关保持开启时不落 enabled 字段,与「未表态即开启」的默认同形。
  Map<String, dynamic>? _quotaSettingsPayload() {
    final settings = QuotaSettings(
      enabled: _quotaEnabled ? null : false,
      autoIntervalMinutes: int.tryParse(_quotaInterval.text.trim()) ?? 0,
      stopIntervalMinutes: int.tryParse(_quotaStopInterval.text.trim()) ?? 0,
    );
    if (settings.empty) {
      return _initialSettings == null ? null : <String, dynamic>{};
    }
    return settings.toJson();
  }

  /// oauth 凭据提交载荷:null=不是 oauth 形态或编辑态保留原登录态。
  /// 服务端要求 oauth_refresh 必带 refresh_token,所以编辑态只有用户
  /// 重新粘贴了 refresh_token 才整体替换凭据。
  /// kimi / bailian(api_key 形态)走完整 credential 对象携带可选额度凭据;
  /// 服务端按字段合并,留空的密钥/token 都保留原值。
  Map<String, dynamic>? _credentialPayload() {
    if (_isKiro) {
      return {
        'kind': 'kiro_refresh',
        'refresh_token': _refreshToken.text.trim(),
        'profile_arn': _profileArn.text.trim(),
        'region': _authRegion.text.trim().isEmpty
            ? 'us-east-1'
            : _authRegion.text.trim(),
        'api_region': _apiRegion.text.trim(),
        'client_id': _clientId.text.trim(),
        'client_secret': _clientSecret.text.trim(),
        if (_kiroAccessToken.isNotEmpty) 'access_token': _kiroAccessToken,
        if (_kiroExpiry.isNotEmpty) 'expiry': _kiroExpiry,
      };
    }
    if (providerVendor(_providerId ?? '') == 'bailian') {
      return {
        'kind': 'api_key',
        'api_key': _apiKey.text.trim(),
        if (_isBailianTokenPlan && _bailianAkId.text.trim().isNotEmpty)
          'bailian_access_key_id': _bailianAkId.text.trim(),
        if (_isBailianTokenPlan && _bailianAkSecret.text.trim().isNotEmpty)
          'bailian_access_key_secret': _bailianAkSecret.text.trim(),
      };
    }
    if (providerVendor(_providerId ?? '') == 'kimi') {
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

  Future<void> _submit({bool verifyBailian = false, bool close = true}) async {
    if (_busy) return;
    if (!(_formKey.currentState?.validate() ?? false)) return;
    if (_isBailianTokenPlan) {
      final id = _bailianAkId.text.trim();
      final secret = _bailianAkSecret.text.trim();
      if ((verifyBailian || id.isNotEmpty || secret.isNotEmpty) &&
          ((id.isEmpty && !_hasStoredBailianKeys) ||
              (secret.isEmpty && !_hasStoredBailianKeys))) {
        TopToast.show(context, '请先填入 AccessKey ID 与 Secret', error: true);
        return;
      }
      if ([id, secret].any(
        (v) =>
            v.isNotEmpty &&
            (RegExp(r'^\*+$').hasMatch(v) ||
                RegExp(r'[\s\x00-\x1f\x7f]').hasMatch(v)),
      )) {
        TopToast.show(context, '请填写有效 AccessKey,不能使用脱敏星号', error: true);
        return;
      }
    }
    FocusManager.instance.primaryFocus?.unfocus();
    setState(() => _busy = true);
    try {
      // 与提供商默认一致即视为不覆盖,保持"跟随提供商"语义
      final url = _baseUrl.text.trim();
      final baseUrl = url == (_spec?.baseUrl ?? '') ? '' : url;
      final quotaSettings = _quotaSettingsPayload();
      final credential = _credentialPayload();
      final Account saved;
      if (_isEdit) {
        saved = await widget.client.updateAccount(
          name: _editing!.name,
          providerId: _providerId,
          apiKey: _apiKey.text.trim(),
          baseUrl: baseUrl,
          headers: _headers,
          quotaSettings: quotaSettings,
          enabled: _enabled,
          credential: credential,
          verifyBailian: verifyBailian,
        );
      } else {
        saved = await widget.client.createAccount(
          name: _name.text.trim(),
          providerId: _providerId!,
          apiKey: _apiKey.text.trim(),
          baseUrl: baseUrl,
          headers: _headers,
          quotaSettings: quotaSettings,
          enabled: _enabled,
          credential: credential,
          verifyBailian: verifyBailian,
        );
      }
      if (!mounted) return;
      if (close) {
        widget.onDone(true);
      } else {
        setState(() {
          _editing = saved;
          _savedInPlace = true;
          _name.text = saved.name;
          _apiKey.clear();
          _bailianAkId.clear();
          _bailianAkSecret.clear();
        });
        TopToast.show(context, '额度认证已验证并保存，切页或重启后保留');
      }
    } catch (e) {
      if (!mounted) return;
      showError(context, e);
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  // ── 提供商级联:厂商→计费模式→服务区域→服务类型,选项由清单动态推导,
  // 上级未选下级禁用,换上级重置下级;服务类型级对所有组渲染,单选项组
  // (含 Standard 占位)自动落定且禁用,多类型组放开选择 ──

  List<String> get _vendorOptions =>
      {for (final p in widget.providers) p.displayName}.toList();

  List<String> get _billingOptions => {
    for (final p in widget.providers)
      if (p.displayName == _vendor) p.billingLabel,
  }.toList();

  List<String> get _regionOptions => {
    for (final p in widget.providers)
      if (p.displayName == _vendor && p.billingLabel == _billing) p.regionLabel,
  }.toList();

  List<String> get _planOptions => {
    for (final p in widget.providers)
      if (p.displayName == _vendor &&
          p.billingLabel == _billing &&
          p.regionLabel == _region)
        p.plan,
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
      _plan = null;
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
      _plan = null;
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
      _plan = null;
      _resolveProvider();
    }),
    validator: (v) => v == null ? '请选择服务区域' : null,
  );

  Widget _planDropdown() {
    final options = _planOptions;
    // 单服务类型组(含 Standard 占位)无需选择:值自动落定、下拉禁用;
    // 多类型组才放开选择。该级对所有组渲染,四级级联形态保持完整。
    final single = options.length == 1;
    return StyledDropdownFormField(
      key: const ValueKey('account-provider-plan'),
      value: single ? options.first : _plan,
      enabled: _region != null && !single,
      decoration: const InputDecoration(border: OutlineInputBorder()),
      options: options,
      labelOf: planDisplayLabel,
      onChanged: (v) => setState(() {
        _plan = v;
        _resolveProvider();
      }),
      validator: (v) => v == null ? '请选择服务类型' : null,
    );
  }

  void _leave() {
    if (!_busy) widget.onDone(_savedInPlace);
  }

  @override
  Widget build(BuildContext context) {
    return FormPage(
      breadcrumbs: [
        CrumbLevel('账号', onTap: _leave),
        CrumbLevel(
          _isEdit
              ? '编辑 ${_editing!.name}'
              : widget.copyFrom != null
              ? '拷贝 ${widget.copyFrom!.name}'
              : '新建账号',
        ),
      ],
      avatar: ProviderAvatar(providerId: _providerId ?? '?', size: 56),
      onCancel: _leave,
      onSubmit: _submit,
      submitLabel: _isEdit ? '保存' : '创建',
      busy: _busy,
      child: AbsorbPointer(
        absorbing: _busy,
        child: Focus(
          descendantsAreFocusable: !_busy,
          child: Form(
            key: _formKey,
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                _basicSection(),
                const SizedBox(height: 26),
                HeaderEditor(initial: _headers, onChanged: (h) => _headers = h),
                const SizedBox(height: 26),
                _quotaSection(),
              ],
            ),
          ),
        ),
      ),
    );
  }

  // ── 基本信息(提供商/账号名/请求地址/密钥),收进可折叠分栏 ──

  String get _basicSubtitle {
    final p = widget.providers.where((p) => p.id == _providerId).firstOrNull;
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
          // 账号名/厂商/计费模式/服务区域/服务类型一行一个,账号名居首;
          // 编辑态账号名是资源键不可改,灰框禁用展示;
          // 计费模式/服务区域/服务类型是级联下级,从上至下依次解锁;
          // 服务类型级对所有组渲染:Standard 单选项自动落定、禁用展示
          LabeledField(
            label: '账号名',
            child: TextFormField(
              key: const ValueKey('account-name'),
              controller: _name,
              enabled: !_isEdit,
              decoration: const InputDecoration(border: OutlineInputBorder()),
              validator: (v) =>
                  (v == null || v.trim().isEmpty) ? '账号名不能为空' : null,
            ),
          ),
          const SizedBox(height: 20),
          LabeledField(label: '提供商', child: _vendorDropdown()),
          const SizedBox(height: 20),
          LabeledField(label: '计费模式', child: _billingDropdown()),
          const SizedBox(height: 20),
          LabeledField(label: '服务区域', child: _regionDropdown()),
          const SizedBox(height: 20),
          LabeledField(label: '服务类型', child: _planDropdown()),
          const SizedBox(height: 20),
          LabeledField(
            label: '请求地址',
            hint: '默认跟随提供商；修改后仅本账号生效',
            child: TextFormField(
              key: const ValueKey('account-base-url'),
              controller: _baseUrl,
              decoration: const InputDecoration(border: OutlineInputBorder()),
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
          if ((_isOAuth || _isKiro) && _isEdit && _editing!.needsReauth) ...[
            Container(
              key: const ValueKey('oauth-reauth-banner'),
              padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 10),
              decoration: BoxDecoration(
                color: Theme.of(context).colorScheme.errorContainer,
                borderRadius: BorderRadius.circular(8),
              ),
              child: Text(
                _isKiro
                    ? '登录态已失效,请重新从 Kiro App 导入后保存'
                    : '登录态已失效,请重新粘贴 Refresh Token 后保存',
                style: TextStyle(
                  fontSize: 12.5,
                  color: Theme.of(context).colorScheme.onErrorContainer,
                ),
              ),
            ),
            const SizedBox(height: 20),
          ],
          if (_isKiro)
            ..._kiroFields()
          else if (_isOAuth)
            ..._oauthFields()
          else
            _apiKeyField(),
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
          // 默认纯星号不泄露任何字符;点眼睛从管理面拉完整密钥填入框内,
          // 明文可见可拷贝。拉取失败保持隐藏并报错。
          hintText: _isEdit
              ? _revealKey
                    ? _editing!.maskedApiKey
                    : '************'
              : widget.copyFrom != null
              ? _revealKey
                    ? widget.copyFrom!.maskedApiKey
                    : '************'
              : null,
          border: const OutlineInputBorder(),
          suffixIcon: IconButton(
            tooltip: _revealKey ? '隐藏' : '显示',
            icon: Icon(
              _revealKey
                  ? Icons.visibility_off_outlined
                  : Icons.visibility_outlined,
            ),
            onPressed: _revealKey
                ? () => setState(() => _revealKey = false)
                : _revealApiKey,
          ),
        ),
        validator: (v) {
          if (_isEdit) return null;
          return (v == null || v.trim().isEmpty) ? '密钥不能为空' : null;
        },
      ),
    );
  }

  /// 点眼睛揭示密钥:框里已有内容(用户输入或此前已揭示)只切掩码;
  /// 编辑/拷贝态空框则从管理面拉真实密钥填入,成为可见可选可拷贝的明文。
  /// 填入后保存会把同值写回,不改变凭据。
  Future<void> _revealApiKey() async {
    final sourceName = widget.editing?.name ?? widget.copyFrom?.name;
    if (sourceName == null || _apiKey.text.trim().isNotEmpty) {
      setState(() => _revealKey = true);
      return;
    }
    try {
      final cred = await widget.client.fetchAccountCredential(sourceName);
      if (!mounted) return;
      setState(() {
        final key = cred['api_key'] as String? ?? '';
        if (key.isNotEmpty) _apiKey.text = key;
        _revealKey = true;
      });
    } catch (e) {
      if (mounted) showError(context, e);
    }
  }

  /// kimi 网页会话 token:月度会员额度查询链的凭据。与密钥同款交互——
  /// 默认纯星号、眼睛亮掩码、完整 token 后端从不下发;可选手动粘贴或
  /// 从本机 kimi-desktop 本地存储自动提取。
  Widget _kimiWebTokenField() {
    final masked =
        _editing?.maskedWebRefreshToken ??
        widget.copyFrom?.maskedWebRefreshToken ??
        '';
    final hasToken =
        _webRefreshToken.text.trim().isNotEmpty || masked.isNotEmpty;
    return _loginCard(
      title: '网页会话登录态',
      configured: hasToken,
      configuredHint: '已配置;重新导入或粘贴新值整体替换,留空保留原值。',
      unconfiguredHint: '可选;导入本机 kimi-desktop 的网页会话登录态后,额度页可显示会员月总额度。',
      items: [
        _checkItem(done: hasToken, name: '网页会话 Token', desc: '月度会员额度查询链的凭据'),
      ],
      buttons: [
        OutlinedButton.icon(
          key: const ValueKey('kimi-autofill-desktop'),
          icon: const Icon(Icons.desktop_windows_outlined, size: 18),
          label: const Text('从 kimi-desktop 导入'),
          onPressed: _fillKimiWebToken,
        ),
      ],
      detailsToggleKey: const ValueKey('kimi-web-details-toggle'),
      details: [
        LabeledField(
          label: '网页会话 Token',
          hint:
              '可选;填入后额度页可显示会员月总额度。'
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
                icon: Icon(
                  _revealKey
                      ? Icons.visibility_off_outlined
                      : Icons.visibility_outlined,
                ),
                onPressed: () => setState(() => _revealKey = !_revealKey),
              ),
            ),
            onChanged: (_) => setState(() {}),
          ),
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

  /// Kiro 密钥独立眼睛开关,已存值仅以纯星号占位,从不把掩码写入输入框。
  Widget _kiroField(
    String key,
    String label,
    TextEditingController controller, {
    String? hint,
    bool secret = false,
    bool hasMasked = false,
    String? Function(String?)? validator,
  }) {
    final revealed = _revealedKiroFields.contains(key);
    return LabeledField(
      label: label,
      hint: hint,
      child: TextFormField(
        key: ValueKey(key),
        controller: controller,
        obscureText: secret && !revealed,
        decoration: InputDecoration(
          border: const OutlineInputBorder(),
          hintText: secret && hasMasked ? '************' : null,
          suffixIcon: secret
              ? IconButton(
                  tooltip: revealed ? '隐藏' : '显示',
                  icon: Icon(
                    revealed
                        ? Icons.visibility_off_outlined
                        : Icons.visibility_outlined,
                  ),
                  onPressed: () => setState(() {
                    if (revealed) {
                      _revealedKiroFields.remove(key);
                    } else {
                      _revealedKiroFields.add(key);
                    }
                  }),
                )
              : null,
        ),
        validator: validator,
        onChanged: (_) => setState(() {
          if (key == 'account-refresh-token' ||
              key == 'account-auth-region' ||
              key == 'account-client-id' ||
              key == 'account-client-secret') {
            _kiroAccessToken = '';
            _kiroExpiry = '';
          }
        }),
      ),
    );
  }

  String? _validateKiroSecret(String? value) {
    final text = value?.trim() ?? '';
    if (text.isNotEmpty &&
        (RegExp(r'[\s\x00-\x1f\x7f]').hasMatch(text) ||
            RegExp(r'^\*+$').hasMatch(text))) {
      return '请填写有效凭据,不能使用脱敏星号';
    }
    return null;
  }

  String? _validateKiroClientPair(String? value) {
    final invalid = _validateKiroSecret(value);
    if (invalid != null) return invalid;
    final hasId = _clientId.text.trim().isNotEmpty;
    final hasSecret =
        _clientSecret.text.trim().isNotEmpty ||
        (_hasStoredKiroCredentials &&
            hasId &&
            _clientId.text.trim() == _editing!.clientId &&
            _editing!.maskedClientSecret.isNotEmpty);
    return hasId != hasSecret ? 'SSO Client ID 与 Client Secret 必须一起填写' : null;
  }

  List<Widget> _kiroFields() => [_kiroLoginCard()];

  /// 登录态卡片:清单即导入说明——逐项列出「导入」会写入的内容,状态区分
  /// 待导入/已配置;原始输入框收进「凭据详情」折叠组,供手改或来源不可达
  /// 时手动粘贴兜底。kiro/codex/kimi 三处导入入口共用此版式。
  Widget _loginCard({
    required String title,
    required bool configured,
    required String configuredHint,
    required String unconfiguredHint,
    required List<Widget> items,
    required List<Widget> buttons,
    required Key detailsToggleKey,
    required List<Widget> details,
    String configuredLabel = '已配置',
    String unconfiguredLabel = '未配置',
    String detailsTitle = '凭据详情(导入自动填充,一般无需修改)',
  }) {
    final t = context.tokens;
    return Container(
      decoration: BoxDecoration(
        color: t.surface,
        border: Border.all(color: t.border),
        borderRadius: BorderRadius.circular(12),
      ),
      padding: const EdgeInsets.fromLTRB(20, 18, 20, 8),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Text(
                title,
                style: TextStyle(
                  fontSize: 15,
                  fontWeight: FontWeight.w600,
                  color: t.ink,
                ),
              ),
              const SizedBox(width: 8),
              Container(
                padding: const EdgeInsets.symmetric(
                  horizontal: 10,
                  vertical: 2,
                ),
                decoration: BoxDecoration(
                  color: configured ? t.successSoft : t.bg,
                  borderRadius: BorderRadius.circular(10),
                ),
                child: Text(
                  configured ? configuredLabel : unconfiguredLabel,
                  style: TextStyle(
                    fontSize: 12,
                    fontWeight: FontWeight.w500,
                    color: configured ? t.success : t.faint,
                  ),
                ),
              ),
            ],
          ),
          const SizedBox(height: 8),
          Text(
            configured ? configuredHint : unconfiguredHint,
            style: TextStyle(fontSize: 12.5, color: t.faint, height: 1.55),
          ),
          const SizedBox(height: 10),
          ...items,
          const SizedBox(height: 14),
          Wrap(spacing: 10, runSpacing: 10, children: buttons),
          _DetailsDisclosure(
            toggleKey: detailsToggleKey,
            title: detailsTitle,
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: details,
            ),
          ),
        ],
      ),
    );
  }

  Widget _kiroLoginCard() {
    final source = _editing ?? widget.copyFrom;
    final hasRefresh =
        _refreshToken.text.trim().isNotEmpty ||
        (source?.maskedRefreshToken.isNotEmpty ?? false);
    final hasSso =
        _clientId.text.trim().isNotEmpty &&
        (_clientSecret.text.trim().isNotEmpty ||
            (source?.maskedClientSecret.isNotEmpty ?? false));
    final arn = _profileArn.text.trim();
    final authRegion = _authRegion.text.trim();
    final apiRegion = _apiRegion.text.trim();
    final regionText = apiRegion.isEmpty || apiRegion == authRegion
        ? authRegion
        : '$authRegion / $apiRegion';
    return _loginCard(
      title: 'Kiro 登录态',
      configured: hasRefresh,
      configuredHint: '凭据已落库,msu 独立续期与查询额度,不依赖本机登录状态;换账号登录后点「重新导入」整体替换。',
      unconfiguredHint:
          '导入本机 Kiro App 的登录态后,msu 独立续期与查询额度,不再依赖本机登录状态。\nIAM / Identity Center 账号的 SSO 会话最长 90 天,到期需重新导入一次。',
      items: [
        _checkItem(
          done: hasRefresh,
          name: 'Refresh Token',
          desc: '续期凭据,轮换链由 msu 自持',
        ),
        _checkItem(
          done: hasSso,
          name: 'SSO 客户端凭据',
          desc: 'IAM 账号续期所需的 Client ID + Secret',
        ),
        _checkItem(
          done: arn.isNotEmpty,
          name: 'Profile ARN',
          desc: '额度查询与转发的身份标识',
          value: arn.isNotEmpty
              ? (arn.length > 4 ? '…${arn.substring(arn.length - 4)}' : arn)
              : null,
        ),
        _checkItem(
          done: regionText.isNotEmpty,
          name: '认证 / API 区域',
          desc: '续期与调用端点',
          value: regionText.isNotEmpty ? regionText : null,
        ),
      ],
      buttons: [
        OutlinedButton.icon(
          key: const ValueKey('kiro-autofill-app'),
          icon: Icon(
            hasRefresh ? Icons.refresh : Icons.file_download_outlined,
            size: 18,
          ),
          label: Text(hasRefresh ? '从 Kiro App 重新导入' : '从 Kiro App 导入'),
          onPressed: _fillKiroApp,
        ),
      ],
      detailsToggleKey: const ValueKey('kiro-details-toggle'),
      details: _kiroDetailFields(),
    );
  }

  Widget _checkItem({
    required bool done,
    required String name,
    required String desc,
    String? value,
    String pendingLabel = '待导入',
  }) {
    final t = context.tokens;
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 7),
      child: Row(
        children: [
          Container(
            width: 17,
            height: 17,
            decoration: BoxDecoration(
              shape: BoxShape.circle,
              color: done ? t.success : Colors.transparent,
              border: done ? null : Border.all(color: t.faint, width: 1.2),
            ),
            child: done
                ? const Icon(Icons.check, size: 11, color: Colors.white)
                : null,
          ),
          const SizedBox(width: 10),
          Text(
            name,
            style: TextStyle(
              fontSize: 13,
              fontWeight: FontWeight.w500,
              color: t.ink,
            ),
          ),
          const SizedBox(width: 10),
          Expanded(
            child: Text(
              desc,
              style: TextStyle(fontSize: 12, color: t.faint),
              overflow: TextOverflow.ellipsis,
            ),
          ),
          const SizedBox(width: 10),
          Text(
            done ? (value ?? '已配置') : pendingLabel,
            style: TextStyle(
              fontSize: 12,
              color: done ? t.success : t.faint,
              fontFamily: done && value != null ? AppConst.fontMono : null,
            ),
          ),
        ],
      ),
    );
  }

  List<Widget> _kiroDetailFields() {
    final source = _editing ?? widget.copyFrom;
    return [
      _kiroField(
        'account-refresh-token',
        'Refresh Token',
        _refreshToken,
        secret: true,
        hasMasked: source?.maskedRefreshToken.isNotEmpty ?? false,
        hint:
            'Kiro 刷新令牌;Desktop 登录或 SSO 均可。'
            '${_hasStoredKiroCredentials ? '编辑时留空保留原值' : '必填,由服务端自动续期'}',
        validator: (v) {
          final invalid = _validateKiroSecret(v);
          if (invalid != null) return invalid;
          return !_hasStoredKiroCredentials && (v?.trim().isEmpty ?? true)
              ? 'Refresh Token 不能为空'
              : null;
        },
      ),
      const SizedBox(height: 20),
      _kiroField(
        'account-profile-arn',
        'Profile ARN',
        _profileArn,
        hint: '可选;随 App 导入自动带入',
      ),
      const SizedBox(height: 20),
      _kiroField(
        'account-auth-region',
        '认证区域',
        _authRegion,
        hint: '令牌签发区域,默认 us-east-1',
      ),
      const SizedBox(height: 20),
      _kiroField(
        'account-api-region',
        'API 区域',
        _apiRegion,
        hint: '可选;推理区域可与认证区域不同',
      ),
      const SizedBox(height: 20),
      _kiroField(
        'account-client-id',
        'SSO Client ID',
        _clientId,
        hint: '可选;与 Client Secret 一起填写启用 SSO',
      ),
      const SizedBox(height: 20),
      _kiroField(
        'account-client-secret',
        'SSO Client Secret',
        _clientSecret,
        secret: true,
        hasMasked: source?.maskedClientSecret.isNotEmpty ?? false,
        hint: _hasStoredKiroCredentials ? '编辑时留空保留原值' : 'Desktop 登录无需填写',
        validator: _validateKiroClientPair,
      ),
    ];
  }

  void _applyKiroCredentials(Map<String, String> credential) {
    setState(() {
      _refreshToken.text = credential['refresh_token']!;
      _profileArn.text = credential['profile_arn']!;
      _authRegion.text = credential['region']!;
      _apiRegion.text = credential['api_region']!;
      _clientId.text = credential['client_id']!;
      _clientSecret.text = credential['client_secret']!;
      _kiroAccessToken = credential['access_token']!;
      _kiroExpiry = credential['expiry']!;
      _revealedKiroFields.clear();
    });
    TopToast.show(context, '已填入 Kiro 登录态');
  }

  void _fillKiroApp() {
    try {
      _applyKiroCredentials(loadKiroAppCredentials());
    } on FileSystemException {
      TopToast.show(context, '无法读取 Kiro App 缓存,请先登录 Kiro App', error: true);
    } on FormatException catch (e) {
      TopToast.show(context, e.message.toString(), error: true);
    }
  }

  List<Widget> _oauthFields() => [_codexLoginCard()];

  Widget _codexLoginCard() {
    final masked =
        _editing?.maskedRefreshToken ??
        widget.copyFrom?.maskedRefreshToken ??
        '';
    final hasRefresh =
        _refreshToken.text.trim().isNotEmpty || masked.isNotEmpty;
    final accountId = _accountId.text.trim();
    return _loginCard(
      title: 'Codex 登录态',
      configured: hasRefresh,
      configuredHint: '凭据已落库,msu 独立续期与查询额度,不依赖本机登录状态;换账号登录后重新导入整体替换。',
      unconfiguredHint:
          '导入本机 codex CLI 或 Codex App 的登录态后,msu 独立续期与查询额度,不再依赖本机登录状态。',
      items: [
        _checkItem(
          done: hasRefresh,
          name: 'Refresh Token',
          desc: '续期凭据,轮换链由 msu 自持',
        ),
        _checkItem(
          done: accountId.isNotEmpty,
          name: 'Account ID',
          desc: '登录态里的 ChatGPT account_id',
        ),
      ],
      buttons: [
        OutlinedButton.icon(
          key: const ValueKey('oauth-autofill-cli'),
          icon: const Icon(Icons.terminal_outlined, size: 18),
          label: const Text('从 codex CLI 导入'),
          onPressed: () =>
              _fillFrom('codex CLI', codexCliAuthPath(), parseCodexCliAuth),
        ),
        OutlinedButton.icon(
          key: const ValueKey('oauth-autofill-app'),
          icon: const Icon(Icons.bolt_outlined, size: 18),
          label: const Text('从 Codex App 导入'),
          onPressed: () =>
              _fillFrom('Codex App', codexAppAuthPath(), parseCodexAppAuth),
        ),
      ],
      detailsToggleKey: const ValueKey('oauth-details-toggle'),
      details: _oauthDetailFields(masked),
    );
  }

  List<Widget> _oauthDetailFields(String masked) {
    return [
      LabeledField(
        label: 'Refresh Token',
        hint: _isEdit ? '留空保留原登录态' : '粘贴后由服务端自动续期',
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
              icon: Icon(
                _revealKey
                    ? Icons.visibility_off_outlined
                    : Icons.visibility_outlined,
              ),
              onPressed: () => setState(() => _revealKey = !_revealKey),
            ),
          ),
          validator: (v) {
            if (_isEdit) return null;
            return (v == null || v.trim().isEmpty)
                ? 'Refresh Token 不能为空'
                : null;
          },
          onChanged: (_) => setState(() {}),
        ),
      ),
      const SizedBox(height: 20),
      LabeledField(
        label: 'Account ID',
        hint: '登录态里的 ChatGPT account_id',
        child: TextFormField(
          key: const ValueKey('account-account-id'),
          controller: _accountId,
          decoration: const InputDecoration(border: OutlineInputBorder()),
          validator: (v) =>
              (v == null || v.trim().isEmpty) ? 'Account ID 不能为空' : null,
          onChanged: (_) => setState(() {}),
        ),
      ),
    ];
  }

  /// 从指定来源读登录态填入 refresh_token 与 account_id;缺失/格式错误
  /// 只报该来源。本地几 KB 小文件,同步读避免异步缝隙里表单已销毁。
  void _fillFrom(
    String label,
    String path,
    (String, String) Function(String) parse,
  ) {
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

  // ── 额度查询区(收进可折叠分栏):查询走供应商 Go 内置实现,这里配
  // 实时查询总开关与自动刷新、停刷两个调度间隔 ──

  Widget _quotaSection() {
    return CollapsibleSection(
      icon: Icons.query_stats,
      title: '额度查询',
      subtitle: _quotaEnabled
          ? (_providerId == 'bailian.cn.subscribe.token-plan'
                ? 'ModelSurge 内置签发/续期 · 认证随账号保存'
                : '走供应商内置查询;两个间隔留空走默认值')
          : '已关闭 · 该账号不查询额度',
      initiallyExpanded: _initialSettings?.empty == false,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Row(
            children: [
              Switch(
                key: const ValueKey('quota-enabled'),
                value: _quotaEnabled,
                onChanged: (v) => setState(() => _quotaEnabled = v),
              ),
              const SizedBox(width: 8),
              const Expanded(
                child: Text('启用实时额度查询', style: TextStyle(fontSize: 12.5)),
              ),
            ],
          ),
          const SizedBox(height: 14),
          Row(
            children: [
              Expanded(
                child: TextFormField(
                  key: const ValueKey('quota-interval'),
                  controller: _quotaInterval,
                  enabled: _quotaEnabled,
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
                  key: const ValueKey('quota-stop-interval'),
                  controller: _quotaStopInterval,
                  enabled: _quotaEnabled,
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
          if (providerVendor(_providerId ?? '') == 'kimi') ...[
            const SizedBox(height: 20),
            _kimiWebTokenField(),
          ],
          if (_providerId == 'bailian.cn.subscribe.token-plan') ...[
            const SizedBox(height: 20),
            _bailianAuthFields(),
          ],
        ],
      ),
    );
  }

  bool get _isBailianTokenPlan =>
      _providerId == 'bailian.cn.subscribe.token-plan';

  bool get _hasStoredBailianKeys =>
      _isBailianTokenPlan &&
      _editing?.providerId == _providerId &&
      (_editing?.maskedBailianAccessKeyId.isNotEmpty ?? false) &&
      (_editing?.maskedBailianAccessKeySecret.isNotEmpty ?? false);

  bool get _bailianVerified =>
      _hasStoredBailianKeys &&
      _bailianAkId.text.trim().isEmpty &&
      _bailianAkSecret.text.trim().isEmpty &&
      (_editing?.bailianVerified ?? false);

  Widget _bailianAuthFields() {
    final verified = _bailianVerified;
    const explanation = 'AccessKey → ModelSurge 内置签发 → 账号持久保存 → 失效自动续期';
    return _loginCard(
      title: 'Token Plan 额度认证',
      configured: verified,
      configuredLabel: '已认证',
      unconfiguredLabel: '未认证',
      configuredHint: '$explanation\n认证已随账号保存,切页或重启后保留。',
      unconfiguredHint: '$explanation\n验证并保存会提交完整账号表单;无需环境配置或外部 CLI。',
      items: [
        _checkItem(
          done: _hasStoredBailianKeys,
          name: 'AccessKey',
          desc: '随账号保存',
          pendingLabel: '待保存',
        ),
        _checkItem(
          done: verified,
          name: '额度查询 Token',
          desc: '内置签发/续期',
          value: '已认证',
          pendingLabel: '待认证',
        ),
        _checkItem(
          done: verified,
          name: '认证状态',
          desc: '切页/重启后保留',
          value: '已认证',
          pendingLabel: '待认证',
        ),
      ],
      buttons: [
        OutlinedButton.icon(
          key: const ValueKey('bailian-auth-verify'),
          icon: _busy
              ? const SizedBox(
                  width: 16,
                  height: 16,
                  child: CircularProgressIndicator(strokeWidth: 2),
                )
              : Icon(
                  verified ? Icons.refresh : Icons.verified_user_outlined,
                  size: 18,
                ),
          label: Text(verified ? '重新验证并保存' : '验证并保存'),
          onPressed: _busy
              ? null
              : () => _submit(verifyBailian: true, close: false),
        ),
      ],
      detailsToggleKey: const ValueKey('bailian-details-toggle'),
      detailsTitle: 'AccessKey 详情(随账号保存,编辑时留空保留原值)',
      details: [
        LabeledField(
          label: 'AccessKey ID',
          child: TextFormField(
            key: const ValueKey('bailian-access-key-id'),
            controller: _bailianAkId,
            enabled: !_busy,
            obscureText: !_revealBailianAkId,
            onChanged: (_) => setState(() {}),
            decoration: InputDecoration(
              border: const OutlineInputBorder(),
              hintText: _hasStoredBailianKeys ? '************' : null,
              suffixIcon: IconButton(
                tooltip: _revealBailianAkId ? '隐藏' : '显示',
                icon: Icon(
                  _revealBailianAkId
                      ? Icons.visibility_off_outlined
                      : Icons.visibility_outlined,
                ),
                onPressed: _busy
                    ? null
                    : () => setState(
                        () => _revealBailianAkId = !_revealBailianAkId,
                      ),
              ),
            ),
          ),
        ),
        const SizedBox(height: 12),
        LabeledField(
          label: 'AccessKey Secret',
          child: TextFormField(
            key: const ValueKey('bailian-access-key-secret'),
            controller: _bailianAkSecret,
            enabled: !_busy,
            obscureText: !_revealBailianAkSecret,
            onChanged: (_) => setState(() {}),
            decoration: InputDecoration(
              border: const OutlineInputBorder(),
              hintText: _hasStoredBailianKeys ? '************' : null,
              suffixIcon: IconButton(
                tooltip: _revealBailianAkSecret ? '隐藏' : '显示',
                icon: Icon(
                  _revealBailianAkSecret
                      ? Icons.visibility_off_outlined
                      : Icons.visibility_outlined,
                ),
                onPressed: _busy
                    ? null
                    : () => setState(
                        () => _revealBailianAkSecret = !_revealBailianAkSecret,
                      ),
              ),
            ),
          ),
        ),
      ],
    );
  }

  /// 分钟数值框校验:留空表示沿用后端默认,填了须是范围内的整数。
  static String? Function(String?) _intRangeValidator(int min, int max) {
    return (v) {
      final s = (v ?? '').trim();
      if (s.isEmpty) return null;
      final n = int.tryParse(s);
      if (n == null || n < min || n > max) return '需为 $min-$max 的整数';
      return null;
    };
  }
}

/// 「凭据详情」轻量折叠组:比 CollapsibleSection 更弱的视觉层级(无边框
/// 卡片,单行文字开关),用于卡片内部的低频区。child 全程留在树内
/// (Align heightFactor 裁剪而非卸载),折叠不丢已填内容。
class _DetailsDisclosure extends StatefulWidget {
  const _DetailsDisclosure({
    required this.toggleKey,
    required this.title,
    required this.child,
  });

  final Key toggleKey;
  final String title;
  final Widget child;

  @override
  State<_DetailsDisclosure> createState() => _DetailsDisclosureState();
}

class _DetailsDisclosureState extends State<_DetailsDisclosure>
    with SingleTickerProviderStateMixin {
  late final AnimationController _ctrl = AnimationController(
    duration: const Duration(milliseconds: 200),
    vsync: this,
  );

  @override
  void dispose() {
    _ctrl.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final t = context.tokens;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      mainAxisSize: MainAxisSize.min,
      children: [
        InkWell(
          key: widget.toggleKey,
          borderRadius: BorderRadius.circular(6),
          onTap: () => _ctrl.isDismissed ? _ctrl.forward() : _ctrl.reverse(),
          child: Padding(
            padding: const EdgeInsets.symmetric(vertical: 8),
            child: Row(
              mainAxisSize: MainAxisSize.min,
              children: [
                AnimatedBuilder(
                  animation: _ctrl,
                  builder: (context, _) => Icon(
                    _ctrl.value > 0.5 ? Icons.expand_more : Icons.chevron_right,
                    size: 15,
                    color: t.faint,
                  ),
                ),
                const SizedBox(width: 2),
                Text(
                  widget.title,
                  style: TextStyle(fontSize: 12.5, color: t.faint),
                ),
              ],
            ),
          ),
        ),
        AnimatedBuilder(
          animation: _ctrl,
          builder: (context, child) => ClipRect(
            child: Align(
              alignment: Alignment.topCenter,
              heightFactor: _ctrl.value,
              child: child,
            ),
          ),
          child: Padding(
            padding: const EdgeInsets.only(top: 6, bottom: 6),
            child: widget.child,
          ),
        ),
      ],
    );
  }
}

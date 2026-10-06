import 'dart:convert';
import 'dart:io';

import 'package:flutter/material.dart';
import 'package:sqlite3/sqlite3.dart' hide Row;

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

/// 百炼 CLI 配置路径;只读文件,不调用会自动更新的 bl。
String bailianCliConfigPath() {
  final home = debugBailianHomeOverride ?? userHomeDir();
  return '$home${Platform.pathSeparator}.bailian${Platform.pathSeparator}config.json';
}

@visibleForTesting
String? debugBailianHomeOverride;

/// CLI 默认配置的 access_token 位于顶层;命名配置由 active_config 指向
/// 同级对象(不是 profiles)。只采控制台 token,绝不采 api_key。
String parseBailianConsoleToken(String text) {
  dynamic decoded;
  try {
    decoded = jsonDecode(text);
  } on FormatException {
    throw const FormatException('百炼 CLI 配置格式无效');
  }
  dynamic config = decoded;
  if (decoded is Map) {
    final active = decoded['active_config'];
    if (active != null && active != '' && active != 'default') {
      config = active is String ? decoded[active] : null;
    }
  }
  final token = config is Map ? config['access_token'] : null;
  if (token is String) {
    final trimmed = token.trim();
    if (trimmed.isNotEmpty &&
        !RegExp(r'[\s\x00-\x1f\x7f]').hasMatch(trimmed) &&
        !RegExp(r'^\*+$').hasMatch(trimmed)) {
      return trimmed;
    }
  }
  throw const FormatException('百炼 CLI 配置缺少有效的额度查询 Token');
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
    if (hash is! String ||
        !RegExp(r'^[a-zA-Z0-9_-]{1,128}$').hasMatch(hash)) {
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

Map<String, String> loadKiroAppCredentials() => importKiroCredentialJson(
    jsonEncode(_readKiroCacheDocument('kiro-auth-token.json')));

/// kiro-cli 登录态数据库候选路径;只读探测,绝不写回。
List<String> kiroCliDbCandidates() {
  final home = debugKiroHomeOverride ?? userHomeDir();
  if (home.isEmpty) throw const FormatException('无法定位用户目录');
  final sep = Platform.pathSeparator;
  return [
    [home, '.local', 'share', 'kiro-cli', 'data.sqlite3'].join(sep),
    [home, '.local', 'share', 'amazon-q', 'data.sqlite3'].join(sep),
    [home, 'AppData', 'Local', 'kiro-cli', 'data.sqlite3'].join(sep),
  ];
}

/// kiro-cli SQLite(auth_kv/state 表) → 后端 credential。复刻 KiroaaS 的
/// 加载逻辑:token key 优先级 social > odic > 旧 codewhisperer;设备注册与
/// state 表 profile ARN 仅作缺失字段兜底;直接字段优先。
Map<String, String> loadKiroCliCredentials() {
  final candidates = kiroCliDbCandidates();
  String? path;
  for (final candidate in candidates) {
    if (File(candidate).existsSync()) {
      path = candidate;
      break;
    }
  }
  if (path == null) {
    throw FormatException(
        '未找到 kiro-cli 登录态数据库(已探测 ${candidates.length} 个默认位置)');
  }
  final db = sqlite3.open(path, mode: OpenMode.readOnly);
  try {
    String? valueOf(String table, String key) {
      final rows = db.select('SELECT value FROM $table WHERE key = ?', [key]);
      if (rows.isEmpty) return null;
      final value = rows.first['value'];
      return value is String ? value : null;
    }

    Map<String, dynamic>? jsonValue(String table, String key) {
      final raw = valueOf(table, key);
      if (raw == null) return null;
      try {
        final decoded = jsonDecode(raw);
        return decoded is Map<String, dynamic> ? decoded : null;
      } on FormatException {
        return null;
      }
    }

    Map<String, dynamic>? token;
    for (final key in const [
      'kirocli:social:token',
      'kirocli:odic:token',
      'codewhisperer:odic:token',
    ]) {
      token = jsonValue('auth_kv', key);
      if (token != null) break;
    }
    if (token == null) {
      throw const FormatException('kiro-cli 数据库里没有有效的登录态');
    }

    Map<String, dynamic>? registration;
    for (final key in const [
      'kirocli:odic:device-registration',
      'codewhisperer:odic:device-registration',
    ]) {
      registration = jsonValue('auth_kv', key);
      if (registration != null) break;
    }

    String profileArn = '';
    String apiRegion = '';
    try {
      final profile = jsonValue('state', 'api.codewhisperer.profile');
      final arn = profile?['arn'];
      if (arn is String) {
        profileArn = arn.trim();
        final parts = profileArn.split(':');
        if (parts.length >= 4 &&
            RegExp(r'^[a-z]+-[a-z]+-\d+$').hasMatch(parts[3])) {
          apiRegion = parts[3];
        }
      }
    } on SqliteException {
      // state 表不存在时忽略,凭 token 字段继续。
    }

    String field(Map<String, dynamic>? source, String name) {
      final value = source?[name];
      return value is String ? value.trim() : '';
    }

    // kiro-cli 的 expires_at 可达纳秒精度,截断到微秒再交给 DateTime 解析。
    final expiry = field(token, 'expires_at')
        .replaceFirstMapped(RegExp(r'(\.\d{6})\d+'), (m) => m[1]!);

    return parseKiroCredentialJson(jsonEncode({
      'accessToken': field(token, 'access_token'),
      'refreshToken': field(token, 'refresh_token'),
      'profileArn': field(token, 'profile_arn').isNotEmpty
          ? field(token, 'profile_arn')
          : profileArn,
      'region': field(token, 'region').isNotEmpty
          ? field(token, 'region')
          : field(registration, 'region'),
      'apiRegion': apiRegion,
      'clientId': field(registration, 'client_id'),
      'clientSecret': field(registration, 'client_secret'),
      'expiresAt': expiry,
    }));
  } finally {
    db.close();
  }
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
  // 百炼可选控制台凭据只用于查额度,编辑态留空由服务端保留。
  late final TextEditingController _consoleAccessToken = TextEditingController();
  late final TextEditingController _profileArn = TextEditingController(
      text: widget.editing?.profileArn ?? widget.copyFrom?.profileArn ?? '');
  late final TextEditingController _authRegion = TextEditingController(
      text: widget.editing?.region ?? widget.copyFrom?.region ?? 'us-east-1');
  late final TextEditingController _apiRegion = TextEditingController(
      text: widget.editing?.apiRegion ?? widget.copyFrom?.apiRegion ?? '');
  late final TextEditingController _clientId = TextEditingController(
      text: widget.editing?.clientId ?? widget.copyFrom?.clientId ?? '');
  final _clientSecret = TextEditingController();
  final _kiroCredentialJson = TextEditingController();
  final _revealedKiroFields = <String>{};
  String _kiroAccessToken = '';
  String _kiroExpiry = '';

  late String? _providerId =
      widget.editing?.providerId ?? widget.copyFrom?.providerId;
  // 编辑/拷贝时按已存 providerId 反推级联选项。
  late String? _vendor = _spec?.displayName;
  late String? _billing = _spec?.billingLabel;
  late String? _region = _spec?.regionLabel;
  late String? _plan =
      _spec != null && _spec!.plan.isNotEmpty ? _spec!.plan : null;

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

  // ── 额度查询(走供应商内置实现,这里配总开关与两个调度间隔)──
  late bool _quotaEnabled = _initialSettings?.quotaEnabled ?? true;
  late final TextEditingController _quotaInterval = TextEditingController(
      text: (_initialSettings?.autoIntervalMinutes ?? 0) > 0
          ? '${_initialSettings!.autoIntervalMinutes}'
          : '');
  late final TextEditingController _quotaStopInterval =
      TextEditingController(text: _initialStopInterval());

  bool _revealKey = false;
  bool _busy = false;

  bool get _isEdit => widget.editing != null;

  QuotaSettings? get _initialSettings =>
      widget.editing?.quotaSettings ?? widget.copyFrom?.quotaSettings;

  @override
  void dispose() {
    _name.dispose();
    _apiKey.dispose();
    _refreshToken.dispose();
    _accountId.dispose();
    _webRefreshToken.dispose();
    _consoleAccessToken.dispose();
    _profileArn.dispose();
    _authRegion.dispose();
    _apiRegion.dispose();
    _clientId.dispose();
    _clientSecret.dispose();
    _kiroCredentialJson.dispose();
    _baseUrl.dispose();
    _quotaInterval.dispose();
    _quotaStopInterval.dispose();
    super.dispose();
  }

  /// 停止查询间隔初值:已有配置(编辑/拷贝)按存储值回显,0=留空走后端
  /// 默认;全新表单一上来就填 5(与后端默认一致,让用户看见默认值)。
  String _initialStopInterval() {
    final v = _initialSettings?.stopIntervalMinutes ?? 0;
    if (v > 0) return '$v';
    return (widget.editing == null && widget.copyFrom == null) ? '5' : '';
  }

  /// 当前选中的提供商是否 OAuth 登录态(订阅)凭据形态。
  bool get _isOAuth => _spec?.credential == 'oauth_refresh';
  bool get _isKiro => _spec?.credential == 'kiro_refresh';
  bool get _hasStoredKiroCredentials =>
      widget.editing?.credentialKind == 'kiro_refresh' &&
      widget.editing?.providerId == _providerId;

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
    // 新建:级联选定前无提供商,地址留空,选定后自动带默认值
    return '';
  }

  void _resolveProvider() {
    final plans = _planOptions;
    final effectivePlan =
        plans.length > 1 ? _plan : (plans.isEmpty ? null : plans.first);
    final match = widget.providers
        .where((p) =>
            p.displayName == _vendor &&
            p.billingLabel == _billing &&
            p.regionLabel == _region &&
            p.plan == effectivePlan)
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
        'console_access_token': _consoleAccessToken.text.trim(),
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

  Future<void> _submit() async {
    if (!(_formKey.currentState?.validate() ?? false)) return;
    setState(() => _busy = true);
    try {
      // 与提供商默认一致即视为不覆盖,保持"跟随提供商"语义
      final url = _baseUrl.text.trim();
      final baseUrl = url == (_spec?.baseUrl ?? '') ? '' : url;
      final quotaSettings = _quotaSettingsPayload();
      final credential = _credentialPayload();
      if (_isEdit) {
        await widget.client.updateAccount(
          name: widget.editing!.name,
          providerId: _providerId,
          apiKey: _apiKey.text.trim(),
          baseUrl: baseUrl,
          headers: _headers,
          quotaSettings: quotaSettings,
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
          quotaSettings: quotaSettings,
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

  // ── 提供商级联:厂商→计费模式→服务区域→服务类型,选项由清单动态推导,
  // 上级未选下级禁用,换上级重置下级;服务类型级只在组内含真实类型
  // (非 Standard 占位)时渲染,单 Standard 组隐藏且自动落定 ──

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

  List<String> get _planOptions => {
        for (final p in widget.providers)
          if (p.displayName == _vendor &&
              p.billingLabel == _billing &&
              p.regionLabel == _region)
            p.plan
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
    // 单服务类型组无需选择:值自动落定、下拉禁用;多类型组才放开选择。
    final single = options.length == 1;
    return StyledDropdownFormField(
      key: const ValueKey('account-provider-plan'),
      value: single ? options.first : _plan,
      enabled: _region != null && !single,
      decoration: const InputDecoration(border: OutlineInputBorder()),
      options: options,
      onChanged: (v) => setState(() {
        _plan = v;
        _resolveProvider();
      }),
      validator: (v) => v == null ? '请选择服务类型' : null,
    );
  }

  @override
  Widget build(BuildContext context) {
    return FormPage(
      breadcrumbs: [
        CrumbLevel('账号', onTap: () => widget.onDone(false)),
        CrumbLevel(
          _isEdit
              ? '编辑 ${widget.editing!.name}'
              : widget.copyFrom != null
                  ? '拷贝 ${widget.copyFrom!.name}'
                  : '新建账号',
        ),
      ],
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
            _quotaSection(),
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
          // 账号名/厂商/计费模式/服务区域一行一个,账号名居首;
          // 编辑态账号名是资源键不可改,灰框禁用展示;
          // 计费模式/服务区域/服务类型是级联下级,从上至下依次解锁;
          // 服务类型级对纯 Standard 组隐藏(Standard 只是占位标签)
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
          // 服务类型级只在有真实类型区分时渲染:全 Standard 组无类型可选,
          // 隐藏该级而不是摆一个禁用的"Standard"占位置。
          if (_planOptions.any((o) => o != 'Standard')) ...[
            const SizedBox(height: 20),
            LabeledField(label: '服务类型', child: _planDropdown()),
          ],
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
          if ((_isOAuth || _isKiro) && _isEdit && widget.editing!.needsReauth) ...[
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
          if (_isKiro)
            ..._kiroFields()
          else if (_isOAuth)
            ..._oauthFields()
          else
            _apiKeyField(),
          if (providerVendor(_providerId ?? '') == 'kimi') ...[
            const SizedBox(height: 20),
            _kimiWebTokenField(),
          ],
          if (providerVendor(_providerId ?? '') == 'bailian') ...[
            const SizedBox(height: 20),
            _bailianConsoleTokenField(),
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

  Widget _bailianConsoleTokenField() {
    final masked = widget.editing?.maskedConsoleAccessToken ??
        widget.copyFrom?.maskedConsoleAccessToken ??
        '';
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        LabeledField(
          label: '额度查询 Token',
          hint: '可选;仅用于查询百炼控制台额度,不影响推理。'
              '${_isEdit ? '编辑时留空保留原值' : ''}',
          child: TextFormField(
            key: const ValueKey('account-console-access-token'),
            controller: _consoleAccessToken,
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
          key: const ValueKey('bailian-autofill-cli'),
          icon: const Icon(Icons.terminal_outlined, size: 18),
          label: const Text('从百炼 CLI 获取'),
          onPressed: _fillBailianConsoleToken,
        ),
      ],
    );
  }

  void _fillBailianConsoleToken() {
    const loginHint = '请先运行 bl auth login --console';
    try {
      final token = parseBailianConsoleToken(
          File(bailianCliConfigPath()).readAsStringSync());
      setState(() => _consoleAccessToken.text = token);
      TopToast.show(context, '已填入百炼 CLI 的额度查询 Token');
    } on FileSystemException {
      TopToast.show(context, '无法读取百炼 CLI 配置;$loginHint', error: true);
    } on FormatException {
      // 不回显 JSON/解码异常原文,其中可能含凭据。
      TopToast.show(context, '百炼 CLI 配置无效或缺少额度查询 Token;$loginHint',
          error: true);
    }
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

  /// Kiro 密钥独立眼睛开关,已存值仅以纯星号占位,从不把掩码写入输入框。
  Widget _kiroField(String key, String label, TextEditingController controller,
      {String? hint, bool secret = false, bool hasMasked = false,
      String? Function(String?)? validator}) {
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
                  icon: Icon(revealed
                      ? Icons.visibility_off_outlined
                      : Icons.visibility_outlined),
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
        onChanged: key == 'account-refresh-token' ||
                key == 'account-auth-region' ||
                key == 'account-client-id' ||
                key == 'account-client-secret'
            ? (_) {
                _kiroAccessToken = '';
                _kiroExpiry = '';
              }
            : null,
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
    final hasSecret = _clientSecret.text.trim().isNotEmpty ||
        (_hasStoredKiroCredentials &&
            hasId &&
            _clientId.text.trim() == widget.editing!.clientId &&
            widget.editing!.maskedClientSecret.isNotEmpty);
    return hasId != hasSecret ? 'SSO Client ID 与 Client Secret 必须一起填写' : null;
  }

  List<Widget> _kiroFields() {
    final source = widget.editing ?? widget.copyFrom;
    return [
      _kiroField('account-refresh-token', 'Refresh Token', _refreshToken,
          secret: true,
          hasMasked: source?.maskedRefreshToken.isNotEmpty ?? false,
          hint: 'Kiro 刷新令牌;Desktop 登录或 SSO 均可。'
              '${_hasStoredKiroCredentials ? '编辑时留空保留原值' : '必填,由服务端自动续期'}',
          validator: (v) {
            final invalid = _validateKiroSecret(v);
            if (invalid != null) return invalid;
            return !_hasStoredKiroCredentials && (v?.trim().isEmpty ?? true)
                ? 'Refresh Token 不能为空'
                : null;
          }),
      const SizedBox(height: 20),
      _kiroField('account-profile-arn', 'Profile ARN', _profileArn,
          hint: '可选;Desktop 刷新可自动回填'),
      const SizedBox(height: 20),
      _kiroField('account-auth-region', '认证区域', _authRegion,
          hint: '令牌签发区域,默认 us-east-1'),
      const SizedBox(height: 20),
      _kiroField('account-api-region', 'API 区域', _apiRegion,
          hint: '可选;推理区域可与认证区域不同'),
      const SizedBox(height: 20),
      _kiroField('account-client-id', 'SSO Client ID', _clientId,
          hint: '可选;与 Client Secret 一起填写启用 SSO'),
      const SizedBox(height: 20),
      _kiroField('account-client-secret', 'SSO Client Secret', _clientSecret,
          secret: true,
          hasMasked: source?.maskedClientSecret.isNotEmpty ?? false,
          hint: _hasStoredKiroCredentials ? '编辑时留空保留原值' : 'Desktop 登录无需填写',
          validator: _validateKiroClientPair),
      const SizedBox(height: 12),
      Align(
        alignment: Alignment.centerLeft,
        child: Wrap(
          spacing: 12,
          runSpacing: 12,
          children: [
            OutlinedButton.icon(
              key: const ValueKey('kiro-autofill-app'),
              icon: const Icon(Icons.desktop_windows_outlined, size: 18),
              label: const Text('从 Kiro App 获取'),
              onPressed: _fillKiroApp,
            ),
            OutlinedButton.icon(
              key: const ValueKey('kiro-autofill-cli'),
              icon: const Icon(Icons.terminal_outlined, size: 18),
              label: const Text('从 kiro-cli 获取'),
              onPressed: _fillKiroCli,
            ),
          ],
        ),
      ),
      const SizedBox(height: 20),
      _kiroField('kiro-credential-json', 'Kiro credential JSON', _kiroCredentialJson,
          secret: true, hint: '手动粘贴 Kiro 缓存 JSON 后点击导入;不会写回本机文件'),
      const SizedBox(height: 12),
      Align(
        alignment: Alignment.centerLeft,
        child: OutlinedButton.icon(
          key: const ValueKey('kiro-import-json'),
          icon: const Icon(Icons.file_download_outlined, size: 18),
          label: const Text('导入 Kiro JSON'),
          onPressed: _importKiroJson,
        ),
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
      _kiroCredentialJson.clear();
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

  void _fillKiroCli() {
    try {
      _applyKiroCredentials(loadKiroCliCredentials());
    } on SqliteException {
      TopToast.show(context, '无法读取 kiro-cli 数据库,请先登录 kiro-cli', error: true);
    } on FormatException catch (e) {
      TopToast.show(context, e.message.toString(), error: true);
    }
  }

  void _importKiroJson() {
    try {
      _applyKiroCredentials(importKiroCredentialJson(_kiroCredentialJson.text));
    } on FileSystemException {
      TopToast.show(context, '无法读取 Kiro SSO 注册缓存,请先登录 Kiro App', error: true);
    } on FormatException catch (e) {
      TopToast.show(context, e.message.toString(), error: true);
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

  // ── 额度查询区(收进可折叠分栏):查询走供应商 Go 内置实现,这里配
  // 实时查询总开关与自动刷新、停刷两个调度间隔 ──

  Widget _quotaSection() {
    return CollapsibleSection(
      icon: Icons.query_stats,
      title: '额度查询',
      subtitle: _quotaEnabled
          ? '走供应商内置查询;两个间隔留空走默认值'
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
                child:
                    Text('启用实时额度查询', style: TextStyle(fontSize: 12.5)),
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
        ],
      ),
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

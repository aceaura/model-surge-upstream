/// 客户端与 backend 的唯一 HTTP 出口。
///
/// 三类失败分开建模，让界面能给出可操作提示而不是通用文案：
/// 服务不可达 / 管理密钥无效 / 服务端校验错误。
/// 本文件不打印任何日志——管理密钥会出现在请求头上。
library;

import 'dart:convert';

import 'package:http/http.dart' as http;

import 'models.dart';

sealed class ApiException implements Exception {
  const ApiException(this.message);
  final String message;
  @override
  String toString() => message;
}

/// 服务不可达。携带所用地址，便于运维者核对配置。
class UnreachableException extends ApiException {
  const UnreachableException(this.baseUrl, String message) : super(message);
  final String baseUrl;
}

/// 管理密钥无效。
class UnauthorizedException extends ApiException {
  const UnauthorizedException() : super('管理密钥无效');
}

/// 服务端校验类错误，message 直接取服务返回的说明。
class ValidationException extends ApiException {
  const ValidationException(this.code, super.message, this.status);
  final String code;
  final int status;
}

class ApiClient {
  ApiClient({
    required this.baseUrl,
    required this.adminKey,
    http.Client? httpClient,
  }) : _http = httpClient ?? http.Client();

  final String baseUrl;
  final String adminKey;
  final http.Client _http;

  static const _timeout = Duration(seconds: 15);

  Map<String, String> get _headers => {
        'Authorization': 'Bearer $adminKey',
        'Content-Type': 'application/json',
      };

  Uri _uri(String path, [Map<String, String>? query]) {
    final root = baseUrl.endsWith('/')
        ? baseUrl.substring(0, baseUrl.length - 1)
        : baseUrl;
    return Uri.parse('$root$path').replace(queryParameters: query);
  }

  Future<Map<String, dynamic>> _send(
    String method,
    String path, {
    Map<String, String>? query,
    Map<String, dynamic>? body,
    Duration? timeout,
  }) async {
    final uri = _uri(path, query);
    final request = http.Request(method, uri)..headers.addAll(_headers);
    if (body != null) {
      request.body = jsonEncode(body);
    }

    http.Response response;
    try {
      final streamed =
          await _http.send(request).timeout(timeout ?? _timeout);
      response = await http.Response.fromStream(streamed);
    } catch (e) {
      throw UnreachableException(baseUrl, '无法连接服务：$baseUrl');
    }

    if (response.statusCode == 401) {
      throw const UnauthorizedException();
    }
    if (response.statusCode == 204) {
      return const {};
    }
    if (response.statusCode >= 200 && response.statusCode < 300) {
      if (response.body.isEmpty) return const {};
      return jsonDecode(response.body) as Map<String, dynamic>;
    }
    throw _validationOf(response);
  }

  ValidationException _validationOf(http.Response response) {
    try {
      final decoded = jsonDecode(response.body) as Map<String, dynamic>;
      final error = decoded['error'] as Map<String, dynamic>;
      return ValidationException(
        error['code'] as String? ?? 'unknown',
        error['message'] as String? ?? '请求失败',
        response.statusCode,
      );
    } catch (_) {
      return ValidationException(
          'unknown', '请求失败（HTTP ${response.statusCode}）', response.statusCode);
    }
  }

  Future<List<ProviderSpec>> listProviders() async {
    final body = await _send('GET', '/admin/providers');
    return (body['providers'] as List<dynamic>? ?? const [])
        .map((e) => ProviderSpec.fromJson(e as Map<String, dynamic>))
        .toList();
  }

  Future<List<Account>> listAccounts() async {
    final body = await _send('GET', '/admin/accounts');
    return (body['accounts'] as List<dynamic>? ?? const [])
        .map((e) => Account.fromJson(e as Map<String, dynamic>))
        .toList();
  }

  /// 返回账号与其模型数，模型数用于删除确认文案。
  Future<(Account, int)> getAccount(String name) async {
    final body = await _send('GET', '/admin/accounts/$name');
    return (
      Account.fromJson(body['account'] as Map<String, dynamic>),
      body['model_count'] as int? ?? 0,
    );
  }

  Future<Account> createAccount({
    required String name,
    required String providerId,
    required String apiKey,
    String? baseUrl,
    Map<String, String>? headers,
    Map<String, dynamic>? quotaScript,
    bool enabled = true,
    Map<String, dynamic>? credential,
  }) async {
    final body = await _send('POST', '/admin/accounts', body: {
      'name': name,
      'provider_id': providerId,
      // 完整 credential 对象(oauth_refresh 等)优先于 api_key 简写。
      if (credential != null) 'credential': credential else 'api_key': apiKey,
      'base_url': ?baseUrl,
      'headers': ?headers,
      'quota_script': ?quotaScript,
      'enabled': enabled,
    });
    return Account.fromJson(body['account'] as Map<String, dynamic>);
  }

  /// apiKey 为空表示保留服务端已存的凭据。
  /// credential(oauth 登录态)非空时整体替换凭据,优先于 apiKey。
  /// quotaScript 为 null 表示保留原脚本配置;传空 Map 表示清除。
  Future<Account> updateAccount({
    required String name,
    String? providerId,
    String? apiKey,
    String? baseUrl,
    Map<String, String>? headers,
    Map<String, dynamic>? quotaScript,
    required bool enabled,
    Map<String, dynamic>? credential,
  }) async {
    final body = await _send('PUT', '/admin/accounts/$name', body: {
      'provider_id': ?providerId,
      if (credential != null)
        'credential': credential
      else if (apiKey != null && apiKey.isNotEmpty)
        'api_key': apiKey,
      'base_url': ?baseUrl,
      'headers': ?headers,
      'quota_script': ?quotaScript,
      'enabled': enabled,
    });
    return Account.fromJson(body['account'] as Map<String, dynamic>);
  }

  Future<List<String>> deleteAccount(String name) async {
    final body = await _send('DELETE', '/admin/accounts/$name');
    return (body['deleted_models'] as List<dynamic>? ?? const []).cast<String>();
  }

  Future<List<UpstreamModel>> listModels({String? account}) async {
    final body = await _send('GET', '/admin/models',
        query: account == null ? null : {'account': account});
    return (body['models'] as List<dynamic>? ?? const [])
        .map((e) => UpstreamModel.fromJson(e as Map<String, dynamic>))
        .toList();
  }

  Future<UpstreamModel> createModel({
    required String id,
    required String account,
    required String nativeModel,
    required String protocol,
    required int contextWindow,
    required Map<String, dynamic> defaults,
    required Map<String, dynamic> overrides,
    Map<String, dynamic>? compact,
    bool enabled = true,
  }) async {
    final body = await _send('POST', '/admin/models', body: {
      'id': id,
      'account': account,
      'native_model': nativeModel,
      'protocol': protocol,
      'context_window': contextWindow,
      'defaults': defaults,
      'overrides': overrides,
      'compact': ?compact,
      'enabled': enabled,
    });
    return UpstreamModel.fromJson(body['model'] as Map<String, dynamic>);
  }

  Future<UpstreamModel> updateModel({
    required String id,
    String? account,
    String? nativeModel,
    String? protocol,
    int? contextWindow,
    Map<String, dynamic>? defaults,
    Map<String, dynamic>? overrides,
    Map<String, dynamic>? compact,
    required bool enabled,
  }) async {
    final body = await _send('PUT', '/admin/models/$id', body: {
      'account': ?account,
      'native_model': ?nativeModel,
      'protocol': ?protocol,
      'context_window': ?contextWindow,
      'defaults': ?defaults,
      'overrides': ?overrides,
      'compact': ?compact,
      'enabled': enabled,
    });
    return UpstreamModel.fromJson(body['model'] as Map<String, dynamic>);
  }

  Future<void> deleteModel(String id) => _send('DELETE', '/admin/models/$id');

  /// 检测命名模型到真实上游的连通性。服务端恒回 200：链路/凭据失败是
  /// 检测结果而非请求错误。超时放宽到 15s（服务端探测上限 10s + 余量）。
  Future<ModelTestResult> testModel(String id) async {
    final body = await _send(
      'POST',
      '/admin/model-test/$id', // id 含 /，与 deleteModel 同款不编码
      timeout: const Duration(seconds: 15),
    );
    return ModelTestResult.fromJson(body);
  }

  /// 检测账号生效请求地址的可达性。服务端恒回 200：不可达是检测结果
  /// 而非请求错误。注意 ok 口径与 testModel 不同——拿到任意 HTTP 响应
  /// 即 ok（可达 ≠ 凭据正确；CC Switch 同款语义）。超时同 testModel。
  Future<ModelTestResult> testAccount(String name) async {
    final body = await _send(
      'POST',
      '/admin/account-test/$name', // name 可含 /，与 testModel 同款不编码
      timeout: const Duration(seconds: 15),
    );
    return ModelTestResult.fromJson(body);
  }

  /// force=true 让服务端丢弃进程内额度缓存再查(行内刷新钮手动重查);
  /// 默认走缓存,自动轮询与首次加载不必每次都打上游。
  /// auto=true 标记这是定时轮询:账号空闲(窗口内无转发/对话请求)时
  /// 服务端直接回过缓存不打上游,直到下一次请求到达自动恢复。
  Future<QuotaReport> queryQuota(String account,
      {bool force = false, bool auto = false}) async {
    final params = [if (force) 'refresh=1', if (auto) 'auto=1'];
    final qs = params.isEmpty ? '' : '?${params.join('&')}';
    final body = await _send('GET', '/admin/accounts/$account/quota$qs');
    return QuotaReport.fromJson(body);
  }

  /// 用账号内置凭据试跑一段未落库的额度脚本(保存前验证代码与渠道
  /// 端点)。服务端恒回 200:脚本/上游失败是试跑结果(ok=false+error)
  /// 而非请求错误。超时放宽到 130s(脚本超时上限 120s + 余量)。
  Future<(bool, String, QuotaReport?)> testQuotaScript(
    String name, {
    required String code,
    int timeoutSeconds = 0,
  }) async {
    final body = await _send(
      'POST',
      '/admin/accounts/$name/quota-test',
      body: {'code': code, 'timeout_seconds': timeoutSeconds},
      timeout: const Duration(seconds: 130),
    );
    final ok = body['ok'] as bool? ?? false;
    final error = body['error'] as String? ?? '';
    final raw = body['report'];
    final report = raw is Map<String, dynamic> ? QuotaReport.fromJson(raw) : null;
    return (ok, error, report);
  }

  Future<ProxySettings> getProxySettings() async {
    final body = await _send('GET', '/admin/proxy-settings');
    return ProxySettings.fromJson(body['settings'] as Map<String, dynamic>);
  }

  /// 全量替换代理转发面配置，服务端立即应用（重绑监听）。
  Future<ProxySettings> updateProxySettings({
    required String apiKey,
    required int port,
    required bool lanOpen,
  }) async {
    final body = await _send('PUT', '/admin/proxy-settings', body: {
      'api_key': apiKey,
      'port': port,
      'lan_open': lanOpen,
    });
    return ProxySettings.fromJson(body['settings'] as Map<String, dynamic>);
  }

  /// 增量拉取进程日志：since 之后（不含）的条目 + 服务端当前尾 seq。
  Future<(List<LogEntry>, int)> fetchLogs(int since) async {
    final body =
        await _send('GET', '/admin/logs', query: {'since': '$since'});
    final entries = (body['entries'] as List<dynamic>? ?? const [])
        .map((e) => LogEntry.fromJson(e as Map<String, dynamic>))
        .toList();
    return (entries, body['next'] as int? ?? 0);
  }

  Future<void> clearLogs() => _send('DELETE', '/admin/logs');

  Future<List<ChatSession>> listChatSessions() async {
    final body = await _send('GET', '/admin/chat/sessions');
    return (body['sessions'] as List<dynamic>? ?? const [])
        .map((e) => ChatSession.fromJson(e as Map<String, dynamic>))
        .toList();
  }

  Future<ChatSession> createChatSession() async {
    final body = await _send('POST', '/admin/chat/sessions');
    return ChatSession.fromJson(body['session'] as Map<String, dynamic>);
  }

  /// 手工改会话标题：服务端去空白并限长，返回更新后的会话。
  Future<ChatSession> renameChatSession(String id, String title) async {
    final body = await _send(
      'PATCH',
      '/admin/chat/sessions/$id',
      body: {'title': title},
    );
    return ChatSession.fromJson(body['session'] as Map<String, dynamic>);
  }

  Future<void> deleteChatSession(String id) =>
      _send('DELETE', '/admin/chat/sessions/$id');

  Future<(ChatSession, List<ChatMessage>)> getChatMessages(String id) async {
    final body = await _send('GET', '/admin/chat/sessions/$id/messages');
    final msgs = (body['messages'] as List<dynamic>? ?? const [])
        .map((e) => ChatMessage.fromJson(e as Map<String, dynamic>))
        .toList();
    return (
      ChatSession.fromJson(body['session'] as Map<String, dynamic>),
      msgs,
    );
  }

  Future<void> clearChatMessages(String id) =>
      _send('DELETE', '/admin/chat/sessions/$id/messages');

  /// 发送一轮对话：用户消息落库 → 服务端带上游补全 → 返回整段消息。
  /// images 为内嵌图片附件（base64，纯图消息 content 可为空）。
  /// effort 为推理档（空=默认，仅 responses/chat_completions 协议生效）。
  /// 超时放宽到 200s：长回复模型的整轮补全远超默认 15s。
  Future<List<ChatMessage>> sendChatMessage(
    String id, {
    required String modelId,
    required String content,
    String effort = '',
    List<ChatAttachment> images = const [],
  }) async {
    final body = await _send(
      'POST',
      '/admin/chat/sessions/$id/messages',
      body: {
        'model_id': modelId,
        'content': content,
        if (effort.isNotEmpty) 'effort': effort,
        if (images.isNotEmpty) 'images': images.map((e) => e.toJson()).toList(),
      },
      timeout: const Duration(seconds: 200),
    );
    return (body['messages'] as List<dynamic>? ?? const [])
        .map((e) => ChatMessage.fromJson(e as Map<String, dynamic>))
        .toList();
  }

  /// 用量查询的公共过滤参数：时间区间 + 模型/账号/来源过滤。
  Map<String, String> _usageQuery({
    DateTime? start,
    DateTime? end,
    String? model,
    String? account,
    String? source,
    String? granularity,
    int? limit,
    int? offset,
  }) {
    final q = <String, String>{};
    if (start != null) q['start'] = start.toUtc().toIso8601String();
    if (end != null) q['end'] = end.toUtc().toIso8601String();
    if (model != null && model.isNotEmpty) q['model'] = model;
    if (account != null && account.isNotEmpty) q['account'] = account;
    if (source != null && source.isNotEmpty) q['source'] = source;
    if (granularity != null) q['granularity'] = granularity;
    if (limit != null) q['limit'] = '$limit';
    if (offset != null) q['offset'] = '$offset';
    return q;
  }

  /// 区间总指标：请求数/成功数/四桶/真实消耗/命中率。
  Future<UsageTotals> usageSummary({
    DateTime? start,
    DateTime? end,
    String? model,
    String? account,
    String? source,
  }) async {
    final body = await _send('GET', '/admin/usage/summary',
        query: _usageQuery(
            start: start, end: end, model: model, account: account, source: source));
    return UsageTotals.fromJson(body);
  }

  /// 趋势分桶。granularity 传 hour/day；不传由服务端按跨度自动。
  Future<(String, List<UsageBucket>)> usageTrend({
    DateTime? start,
    DateTime? end,
    String? model,
    String? account,
    String? source,
    String? granularity,
  }) async {
    final body = await _send('GET', '/admin/usage/trend',
        query: _usageQuery(
            start: start,
            end: end,
            model: model,
            account: account,
            source: source,
            granularity: granularity));
    final g = body['granularity'] as String? ?? 'hour';
    final buckets = (body['buckets'] as List<dynamic>? ?? const [])
        .map((e) => UsageBucket.fromJson(e as Map<String, dynamic>))
        .toList();
    return (g, buckets);
  }

  /// 按命名模型聚合。
  Future<List<UsageGroup>> usageModels({
    DateTime? start,
    DateTime? end,
    String? account,
    String? source,
  }) async {
    final body = await _send('GET', '/admin/usage/models',
        query: _usageQuery(
            start: start, end: end, account: account, source: source));
    return (body['models'] as List<dynamic>? ?? const [])
        .map((e) => UsageGroup.fromJson(e as Map<String, dynamic>))
        .toList();
  }

  /// 按账号聚合。
  Future<List<UsageGroup>> usageAccounts({
    DateTime? start,
    DateTime? end,
    String? model,
    String? source,
  }) async {
    final body = await _send('GET', '/admin/usage/accounts',
        query: _usageQuery(start: start, end: end, model: model, source: source));
    return (body['accounts'] as List<dynamic>? ?? const [])
        .map((e) => UsageGroup.fromJson(e as Map<String, dynamic>))
        .toList();
  }

  /// 请求明细分页。
  Future<(List<UsageLogRow>, int)> usageLogs({
    DateTime? start,
    DateTime? end,
    String? model,
    String? account,
    String? source,
    int limit = 50,
    int offset = 0,
  }) async {
    final body = await _send('GET', '/admin/usage/logs',
        query: _usageQuery(
            start: start,
            end: end,
            model: model,
            account: account,
            source: source,
            limit: limit,
            offset: offset));
    final logs = (body['logs'] as List<dynamic>? ?? const [])
        .map((e) => UsageLogRow.fromJson(e as Map<String, dynamic>))
        .toList();
    return (logs, (body['total'] as num?)?.toInt() ?? 0);
  }

  void close() => _http.close();
}

/// 与 backend 接口对应的数据类型。凭据字段只以脱敏形态出现在客户端。
library;

/// token 数 → k 单位字符串(不带后缀):1k = 1000 tokens。
/// 整千给整数(256000 → "256");非整千保留 0.001k 精度并去尾零
/// (131072 → "131.072"),避免编辑回显时丢精度;0 → "0"(未声明)。
String tokensToK(int tokens) {
  if (tokens % 1000 == 0) return '${tokens ~/ 1000}';
  var s = (tokens / 1000).toStringAsFixed(3);
  while (s.endsWith('0')) {
    s = s.substring(0, s.length - 1);
  }
  if (s.endsWith('.')) s = s.substring(0, s.length - 1);
  return s;
}

/// k 单位输入串 → token 数(1k = 1000 tokens,四舍五入到个位)。
int kToTokens(String kText) => (double.parse(kText.trim()) * 1000).round();

class ProviderSpec {
  const ProviderSpec({
    required this.id,
    required this.displayName,
    required this.website,
    required this.baseUrl,
    required this.protocols,
    required this.auth,
    required this.credential,
    required this.billing,
    required this.region,
    required this.quotaKind,
    required this.quotaUnit,
    required this.quotaReset,
    required this.quotaQueryable,
  });

  final String id;
  final String displayName;
  final String website;
  final String baseUrl;
  final List<String> protocols;
  final String auth;
  final String credential;

  /// 计费模式:subscription=订阅,paygo=按量计费。
  final String billing;

  /// 计费模式的中文名,未识别值原样透传。
  String get billingLabel => switch (billing) {
        'subscription' => '订阅',
        'paygo' => '按量计费',
        _ => billing,
      };

  /// 服务区域:CN/Global 或厂商自定义分区,存储英文。
  final String region;

  /// 服务区域的中文名,未识别值原样透传。
  String get regionLabel => switch (region) {
        'CN' => '中国',
        'Global' => '全球',
        _ => region,
      };

  /// 缩写标签:Global 原样,其他区域加小写 -区域 后缀(如 ark-cn);
  /// id 本身已带该后缀(如 kimi-cn)时不重复拼。
  String get tagLabel {
    if (region == 'Global' || region.isEmpty) return id;
    final suffix = '-${region.toLowerCase()}';
    return id.endsWith(suffix) ? id : '$id$suffix';
  }

  /// 类型签:计费模式 · 服务区域(如 订阅 · 全球),分组卡片分节标题用。
  String get typeLabel => '$billingLabel · $regionLabel';

  /// 后端只要带 quota 块即视为声明了额度接口;kind/unit 只是
  /// 主计量项形态说明,老数据可能缺省,不能拿它们当可查询判据。
  final String? quotaKind;
  final String? quotaUnit;
  final String? quotaReset;

  /// 提供商是否声明了额度查询接口(响应里有 quota 块)。
  final bool quotaQueryable;

  factory ProviderSpec.fromJson(Map<String, dynamic> json) {
    final quota = json['quota'] as Map<String, dynamic>?;
    return ProviderSpec(
      id: json['id'] as String,
      displayName: json['display_name'] as String? ?? '',
      website: json['website'] as String? ?? '',
      baseUrl: json['base_url'] as String? ?? '',
      protocols:
          (json['protocols'] as List<dynamic>? ?? const []).cast<String>(),
      auth: json['auth'] as String? ?? '',
      credential: json['credential'] as String? ?? '',
      billing: json['billing'] as String? ?? '',
      region: json['region'] as String? ?? '',
      quotaKind: quota?['kind'] as String?,
      quotaUnit: quota?['unit'] as String?,
      quotaReset: quota?['reset'] as String?,
      quotaQueryable: quota != null,
    );
  }
}

/// 账号级额度查询脚本(仿 CC Switch usage_script):enabled+code 生效,
/// 其余两项为 0 时走后端默认(超时 10s,不自动刷新)。
class QuotaScript {
  const QuotaScript({
    required this.enabled,
    required this.code,
    this.timeoutSeconds = 0,
    this.autoIntervalMinutes = 0,
  });

  final bool enabled;
  final String code;
  final int timeoutSeconds;
  final int autoIntervalMinutes;

  /// 与后端 Active 同口径:启用且代码非空才真正接管额度查询。
  bool get active => enabled && code.isNotEmpty;

  factory QuotaScript.fromJson(Map<String, dynamic> json) => QuotaScript(
        enabled: json['enabled'] as bool? ?? false,
        code: json['code'] as String? ?? '',
        timeoutSeconds: (json['timeout_seconds'] as num?)?.toInt() ?? 0,
        autoIntervalMinutes:
            (json['auto_interval_minutes'] as num?)?.toInt() ?? 0,
      );

  Map<String, dynamic> toJson() => {
        'enabled': enabled,
        'code': code,
        'timeout_seconds': timeoutSeconds,
        'auto_interval_minutes': autoIntervalMinutes,
      };
}

class Account {
  const Account({
    required this.name,
    required this.providerId,
    required this.maskedApiKey,
    required this.baseUrl,
    required this.headers,
    required this.enabled,
    this.quotaScript,
  });

  final String name;
  final String providerId;

  /// 服务端已脱敏的密钥，仅供显示。
  final String maskedApiKey;
  final String baseUrl;
  final Map<String, String> headers;
  final bool enabled;

  /// 额度脚本配置;null 表示未配置(服务端空脚本不回传)。
  final QuotaScript? quotaScript;

  factory Account.fromJson(Map<String, dynamic> json) {
    final credential = json['credential'] as Map<String, dynamic>? ?? const {};
    final headers = json['headers'] as Map<String, dynamic>? ?? const {};
    final script = json['quota_script'] as Map<String, dynamic>?;
    return Account(
      name: json['name'] as String,
      providerId: json['provider_id'] as String? ?? '',
      maskedApiKey: credential['api_key'] as String? ?? '',
      baseUrl: json['base_url'] as String? ?? '',
      headers: headers.map((k, v) => MapEntry(k, '$v')),
      enabled: json['enabled'] as bool? ?? false,
      quotaScript: script == null ? null : QuotaScript.fromJson(script),
    );
  }
}

class UpstreamModel {
  const UpstreamModel({
    required this.id,
    required this.account,
    required this.nativeModel,
    required this.protocol,
    required this.contextWindow,
    required this.defaults,
    required this.overrides,
    required this.compact,
    required this.enabled,
  });

  final String id;
  final String account;
  final String nativeModel;
  final String protocol;
  final int contextWindow;
  final Map<String, dynamic> defaults;
  final Map<String, dynamic> overrides;

  /// 上下文压缩配置:{mode: passive|error|auto, threshold, keep_turns,
  /// max_summary_tokens}。passive=只记录不生效。
  final Map<String, dynamic> compact;
  final bool enabled;

  factory UpstreamModel.fromJson(Map<String, dynamic> json) => UpstreamModel(
        id: json['id'] as String,
        account: json['account'] as String? ?? '',
        nativeModel: json['native_model'] as String? ?? '',
        protocol: json['protocol'] as String? ?? '',
        contextWindow: json['context_window'] as int? ?? 0,
        defaults: json['defaults'] as Map<String, dynamic>? ?? const {},
        overrides: json['overrides'] as Map<String, dynamic>? ?? const {},
        compact: json['compact'] as Map<String, dynamic>? ?? const {},
        enabled: json['enabled'] as bool? ?? false,
      );
}

/// 一条计量项。上游额度语义不止一种：预付费看余量，后付费只有已用量，
/// 速率窗口下 requests 与 tokens 是两条独立计数。
class QuotaMeter {
  const QuotaMeter({
    required this.kind,
    required this.unit,
    this.label,
    this.currency,
    this.remaining,
    this.total,
    this.used,
    this.reset,
    this.resetAt,
    this.extra,
  });

  final String kind;
  final String unit;
  final String? label;
  final String? currency;
  final double? remaining;
  final double? total;
  final double? used;
  final String? reset;
  final DateTime? resetAt;

  /// 脚本提取器附带的自由文本(套餐说明、到期日等),原样透传。
  final String? extra;

  factory QuotaMeter.fromJson(Map<String, dynamic> json) => QuotaMeter(
        kind: json['kind'] as String? ?? '',
        unit: json['unit'] as String? ?? '',
        label: json['label'] as String?,
        currency: json['currency'] as String?,
        remaining: (json['remaining'] as num?)?.toDouble(),
        total: (json['total'] as num?)?.toDouble(),
        used: (json['used'] as num?)?.toDouble(),
        reset: json['reset'] as String?,
        resetAt: DateTime.tryParse(json['reset_at'] as String? ?? ''),
        extra: json['extra'] as String?,
      );

  static const _kindNames = {
    'balance': '余额',
    'usage': '用量',
    'rate_limit': '速率',
  };

  static const _unitNames = {
    'currency': '金额',
    'requests': '请求数',
    'tokens': 'token',
    'credits': '点数',
    'percent': '%',
  };

  String get title {
    final l = label;
    if (l != null && l.isNotEmpty) {
      return '${_kindNames[kind] ?? kind}·$l';
    }
    return '${_kindNames[kind] ?? kind}·${_unitNames[unit] ?? unit}';
  }

  String amount(double? value) {
    if (value == null) return '上游未提供';
    // 百分比紧贴数字(64%),其余单位空一格(12.34 CNY)
    if (unit == 'percent') return '${_trimNum(value)}%';
    final suffix = unit == 'currency'
        ? (currency ?? '')
        : (_unitNames[unit] ?? unit);
    return suffix.isEmpty ? '$value' : '$value $suffix';
  }

  /// 整数不显小数点(64 → "64",64.5 → "64.5")。
  static String _trimNum(double v) =>
      v == v.roundToDouble() ? '${v.round()}' : '$v';
}

class QuotaReport {
  const QuotaReport({
    required this.account,
    required this.queryable,
    required this.meters,
    this.at,
  });

  final String account;
  final bool queryable;
  final List<QuotaMeter> meters;

  /// 服务端查询时刻(行内"N 分钟前"的基准);老数据缺省为 null。
  final DateTime? at;

  factory QuotaReport.fromJson(Map<String, dynamic> json) => QuotaReport(
        account: json['account'] as String? ?? '',
        queryable: json['queryable'] as bool? ?? false,
        meters: (json['meters'] as List<dynamic>? ?? const [])
            .whereType<Map<String, dynamic>>()
            .map(QuotaMeter.fromJson)
            .toList(growable: false),
        at: DateTime.tryParse(json['at'] as String? ?? ''),
      );
}

/// 代理转发面配置。与账号凭据不同，apiKey 从管理面读到的是真实值——
/// 持管理密钥者需要把它拷进第三方客户端，脱敏则无法使用。
class ProxySettings {
  const ProxySettings({
    required this.apiKey,
    required this.port,
    required this.lanOpen,
  });

  final String apiKey;
  final int port;

  /// 是否开放局域网访问：false 仅监听 127.0.0.1，true 监听全部接口。
  final bool lanOpen;

  factory ProxySettings.fromJson(Map<String, dynamic> json) => ProxySettings(
        apiKey: json['api_key'] as String? ?? '',
        port: json['port'] as int? ?? 12344,
        lanOpen: json['lan_open'] as bool? ?? false,
      );
}

/// 进程日志条目。seq 单调递增，客户端用它做增量轮询 cursor。
class LogEntry {
  const LogEntry({
    required this.seq,
    required this.at,
    required this.level,
    required this.source,
    required this.msg,
  });

  final int seq;
  final DateTime at;

  /// info / warn / error。
  final String level;
  final String source;
  final String msg;

  factory LogEntry.fromJson(Map<String, dynamic> json) => LogEntry(
        seq: json['seq'] as int? ?? 0,
        at: DateTime.tryParse(json['at'] as String? ?? '') ?? DateTime.now(),
        level: json['level'] as String? ?? 'info',
        source: json['source'] as String? ?? '',
        msg: json['msg'] as String? ?? '',
      );
}

/// 一次对话。modelId 是最近一次发送所用模型，供选择器回显。
class ChatSession {
  const ChatSession({
    required this.id,
    required this.title,
    required this.modelId,
    required this.updatedAt,
  });

  final String id;
  final String title;
  final String modelId;
  final DateTime updatedAt;

  factory ChatSession.fromJson(Map<String, dynamic> json) => ChatSession(
        id: json['id'] as String? ?? '',
        title: json['title'] as String? ?? '',
        modelId: json['model_id'] as String? ?? '',
        updatedAt:
            DateTime.tryParse(json['updated_at'] as String? ?? '') ??
                DateTime.now(),
      );
}

/// 消息内嵌的一张图片：mime 限 png/jpeg/webp/gif，
/// data 是不带 data: 前缀的 base64（服务端内嵌落库，随消息内联返回）。
class ChatAttachment {
  const ChatAttachment({required this.mime, required this.data});

  final String mime;
  final String data;

  factory ChatAttachment.fromJson(Map<String, dynamic> json) => ChatAttachment(
        mime: json['mime'] as String? ?? '',
        data: json['data'] as String? ?? '',
      );

  Map<String, dynamic> toJson() => {'mime': mime, 'data': data};
}

/// 一条对话消息。role 取 user / assistant。attachments 无图时为空列表。
class ChatMessage {
  const ChatMessage({
    required this.id,
    required this.role,
    required this.content,
    this.attachments = const [],
    required this.createdAt,
  });

  final int id;
  final String role;
  final String content;
  final List<ChatAttachment> attachments;
  final DateTime createdAt;

  factory ChatMessage.fromJson(Map<String, dynamic> json) => ChatMessage(
        id: json['id'] as int? ?? 0,
        role: json['role'] as String? ?? '',
        content: json['content'] as String? ?? '',
        attachments: (json['attachments'] as List<dynamic>? ?? const [])
            .map((e) => ChatAttachment.fromJson(e as Map<String, dynamic>))
            .toList(),
        createdAt:
            DateTime.tryParse(json['created_at'] as String? ?? '') ??
                DateTime.now(),
      );
}

/// 一组用量聚合值。input 是服务端归一后的净输入（已扣缓存）。
class UsageTotals {
  const UsageTotals({
    required this.requests,
    required this.success,
    required this.input,
    required this.output,
    required this.cacheRead,
    required this.cacheWrite,
    required this.realTotal,
    required this.hitRate,
  });

  final int requests;
  final int success;
  final int input;
  final int output;
  final int cacheRead;
  final int cacheWrite;
  final int realTotal;

  /// 缓存命中率 0..1。
  final double hitRate;

  static int _i(Map<String, dynamic> j, String k) =>
      (j[k] as num?)?.toInt() ?? 0;

  factory UsageTotals.fromJson(Map<String, dynamic> json) => UsageTotals(
        requests: _i(json, 'requests'),
        success: _i(json, 'success'),
        input: _i(json, 'input_tokens'),
        output: _i(json, 'output_tokens'),
        cacheRead: _i(json, 'cache_read_tokens'),
        cacheWrite: _i(json, 'cache_write_tokens'),
        realTotal: _i(json, 'real_total_tokens'),
        hitRate: (json['cache_hit_rate'] as num?)?.toDouble() ?? 0,
      );
}

/// 趋势图的一个时间桶（按小时或按天）。
class UsageBucket {
  const UsageBucket({required this.bucket, required this.totals});

  final DateTime bucket;
  final UsageTotals totals;

  factory UsageBucket.fromJson(Map<String, dynamic> json) => UsageBucket(
        bucket: DateTime.tryParse(json['bucket'] as String? ?? '') ??
            DateTime.now(),
        totals: UsageTotals.fromJson(json),
      );
}

/// 按模型或按账号的一行聚合。
class UsageGroup {
  const UsageGroup({required this.key, required this.totals});

  final String key;
  final UsageTotals totals;

  factory UsageGroup.fromJson(Map<String, dynamic> json) => UsageGroup(
        key: json['key'] as String? ?? '',
        totals: UsageTotals.fromJson(json),
      );
}

/// 用量明细行：一次上游请求的四桶与状态。
class UsageLogRow {
  const UsageLogRow({
    required this.id,
    required this.source,
    required this.protocol,
    required this.modelId,
    required this.account,
    required this.nativeModel,
    required this.input,
    required this.output,
    required this.cacheRead,
    required this.cacheWrite,
    required this.statusCode,
    required this.isStreaming,
    required this.latencyMs,
    required this.durationMs,
    required this.errorMessage,
    required this.createdAt,
  });

  final int id;
  final String source;
  final String protocol;
  final String modelId;
  final String account;
  final String nativeModel;
  final int input;
  final int output;
  final int cacheRead;
  final int cacheWrite;
  final int statusCode;
  final bool isStreaming;
  final int? latencyMs;
  final int? durationMs;
  final String errorMessage;
  final DateTime createdAt;

  factory UsageLogRow.fromJson(Map<String, dynamic> json) => UsageLogRow(
        id: json['id'] as int? ?? 0,
        source: json['source'] as String? ?? '',
        protocol: json['protocol'] as String? ?? '',
        modelId: json['model_id'] as String? ?? '',
        account: json['account'] as String? ?? '',
        nativeModel: json['native_model'] as String? ?? '',
        input: (json['input_tokens'] as num?)?.toInt() ?? 0,
        output: (json['output_tokens'] as num?)?.toInt() ?? 0,
        cacheRead: (json['cache_read_tokens'] as num?)?.toInt() ?? 0,
        cacheWrite: (json['cache_write_tokens'] as num?)?.toInt() ?? 0,
        statusCode: json['status_code'] as int? ?? 0,
        isStreaming: json['is_streaming'] as bool? ?? false,
        latencyMs: (json['latency_ms'] as num?)?.toInt(),
        durationMs: (json['duration_ms'] as num?)?.toInt(),
        errorMessage: json['error_message'] as String? ?? '',
        createdAt:
            DateTime.tryParse(json['created_at'] as String? ?? '') ??
                DateTime.now(),
      );
}

/// 模型/账号连通性检测共用的结果结构。ok 口径由检测级别决定:模型级仅
/// 上游回 2xx 为真;账号级拿到任意 HTTP 响应即为真(statusCode 仍带回供
/// 展示,可达 ≠ 凭据正确)。网络级失败时 statusCode 为 0,原因在 error。
class ModelTestResult {
  const ModelTestResult({
    required this.ok,
    required this.statusCode,
    required this.latencyMs,
    required this.error,
  });

  final bool ok;
  final int statusCode;
  final int latencyMs;
  final String error;

  factory ModelTestResult.fromJson(Map<String, dynamic> json) =>
      ModelTestResult(
        ok: json['ok'] as bool? ?? false,
        statusCode: (json['status_code'] as num?)?.toInt() ?? 0,
        latencyMs: (json['latency_ms'] as num?)?.toInt() ?? 0,
        error: json['error'] as String? ?? '',
      );
}

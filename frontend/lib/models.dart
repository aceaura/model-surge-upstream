/// 与 backend 接口对应的数据类型。凭据字段只以脱敏形态出现在客户端。
library;

class ProviderSpec {
  const ProviderSpec({
    required this.id,
    required this.displayName,
    required this.website,
    required this.baseUrl,
    required this.protocols,
    required this.auth,
    required this.credential,
    required this.quotaKind,
    required this.quotaUnit,
    required this.quotaReset,
  });

  final String id;
  final String displayName;
  final String website;
  final String baseUrl;
  final List<String> protocols;
  final String auth;
  final String credential;

  /// 以下三项为 null 表示该 provider 未声明额度接口。
  final String? quotaKind;
  final String? quotaUnit;
  final String? quotaReset;

  bool get quotaQueryable => quotaKind != null;

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
      quotaKind: quota?['kind'] as String?,
      quotaUnit: quota?['unit'] as String?,
      quotaReset: quota?['reset'] as String?,
    );
  }
}

class Account {
  const Account({
    required this.name,
    required this.providerId,
    required this.maskedApiKey,
    required this.baseUrl,
    required this.headers,
    required this.enabled,
  });

  final String name;
  final String providerId;

  /// 服务端已脱敏的密钥，仅供显示。
  final String maskedApiKey;
  final String baseUrl;
  final Map<String, String> headers;
  final bool enabled;

  factory Account.fromJson(Map<String, dynamic> json) {
    final credential = json['credential'] as Map<String, dynamic>? ?? const {};
    final headers = json['headers'] as Map<String, dynamic>? ?? const {};
    return Account(
      name: json['name'] as String,
      providerId: json['provider_id'] as String? ?? '',
      maskedApiKey: credential['api_key'] as String? ?? '',
      baseUrl: json['base_url'] as String? ?? '',
      headers: headers.map((k, v) => MapEntry(k, '$v')),
      enabled: json['enabled'] as bool? ?? false,
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
    required this.enabled,
  });

  final String id;
  final String account;
  final String nativeModel;
  final String protocol;
  final int contextWindow;
  final Map<String, dynamic> defaults;
  final Map<String, dynamic> overrides;
  final bool enabled;

  factory UpstreamModel.fromJson(Map<String, dynamic> json) => UpstreamModel(
        id: json['id'] as String,
        account: json['account'] as String? ?? '',
        nativeModel: json['native_model'] as String? ?? '',
        protocol: json['protocol'] as String? ?? '',
        contextWindow: json['context_window'] as int? ?? 0,
        defaults: json['defaults'] as Map<String, dynamic>? ?? const {},
        overrides: json['overrides'] as Map<String, dynamic>? ?? const {},
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
    final suffix = unit == 'currency'
        ? (currency ?? '')
        : (_unitNames[unit] ?? unit);
    return suffix.isEmpty ? '$value' : '$value $suffix';
  }
}

class QuotaReport {
  const QuotaReport({
    required this.account,
    required this.queryable,
    required this.meters,
  });

  final String account;
  final bool queryable;
  final List<QuotaMeter> meters;

  factory QuotaReport.fromJson(Map<String, dynamic> json) => QuotaReport(
        account: json['account'] as String? ?? '',
        queryable: json['queryable'] as bool? ?? false,
        meters: (json['meters'] as List<dynamic>? ?? const [])
            .whereType<Map<String, dynamic>>()
            .map(QuotaMeter.fromJson)
            .toList(growable: false),
      );
}

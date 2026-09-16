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
    required this.quotaReset,
  });

  final String id;
  final String displayName;
  final String website;
  final String baseUrl;
  final List<String> protocols;
  final String auth;
  final String credential;

  /// null 表示该 provider 未声明额度接口。
  final String? quotaReset;

  bool get quotaQueryable => quotaReset != null;

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

class QuotaReport {
  const QuotaReport({
    required this.account,
    required this.queryable,
    this.remaining,
    this.total,
    this.currency,
    this.reset,
  });

  final String account;
  final bool queryable;
  final double? remaining;
  final double? total;
  final String? currency;
  final String? reset;

  factory QuotaReport.fromJson(Map<String, dynamic> json) => QuotaReport(
        account: json['account'] as String? ?? '',
        queryable: json['queryable'] as bool? ?? false,
        remaining: (json['remaining'] as num?)?.toDouble(),
        total: (json['total'] as num?)?.toDouble(),
        currency: json['currency'] as String?,
        reset: json['reset'] as String?,
      );
}

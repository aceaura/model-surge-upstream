import 'dart:convert';
import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:msu_admin/api_client.dart';
import 'package:msu_admin/models.dart';
import 'package:msu_admin/pages/account_form.dart';
import 'package:msu_admin/theme.dart';
import 'package:msu_admin/ui/styled_dropdown.dart';

final providers = [
  ProviderSpec.fromJson(const {
    'id': 'deepseek.global.api.standard',
    'display_name': 'DeepSeek',
    'website': 'https://platform.deepseek.com',
    'base_url': 'https://api.deepseek.com',
    'protocols': ['chat_completions'],
    'auth': 'bearer',
    'credential': 'api_key',
    'billing': 'paygo',
    'region': 'Global',
    'plan': 'Standard',
  }),
  ProviderSpec.fromJson(const {
    'id': 'openai.global.api.standard',
    'display_name': 'OpenAI',
    'website': 'https://openai.com',
    'base_url': 'https://api.openai.com',
    'protocols': ['chat_completions', 'responses'],
    'auth': 'bearer',
    'credential': 'api_key',
    'billing': 'paygo',
    'region': 'Global',
    'plan': 'Standard',
  }),
  ProviderSpec.fromJson(const {
    'id': 'openai.global.subscribe.codex',
    'display_name': 'OpenAI',
    'website': 'https://chatgpt.com',
    'base_url': 'https://chatgpt.com/backend-api/codex',
    'protocols': ['responses'],
    'auth': 'bearer',
    'credential': 'oauth_refresh',
    'billing': 'subscription',
    'region': 'Global',
    'plan': 'Standard',
  }),
  ProviderSpec.fromJson(const {
    'id': 'kimi.global.subscribe.coding',
    'display_name': 'Kimi',
    'website': 'https://www.kimi.com',
    'base_url': 'https://api.kimi.com',
    'protocols': ['chat_completions'],
    'auth': 'bearer',
    'credential': 'api_key',
    'billing': 'subscription',
    'region': 'CN',
    'plan': 'Standard',
  }),
  ProviderSpec.fromJson(const {
    'id': 'bailian.cn.subscribe.token-plan',
    'display_name': '百炼',
    'base_url': 'https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode',
    'protocols': ['chat_completions'],
    'auth': 'bearer',
    'credential': 'api_key',
    'billing': 'subscription',
    'region': 'CN',
    'plan': 'Token Plan',
    'quota_queryable': true,
  }),
  ProviderSpec.fromJson(const {
    'id': 'bailian.cn.subscribe.coding-plan',
    'display_name': '百炼',
    'base_url': 'https://coding-plan.example.com/v1',
    'protocols': ['anthropic'],
    'auth': 'bearer',
    'credential': 'api_key',
    'billing': 'subscription',
    'region': 'CN',
    'plan': 'Coding Plan',
  }),
  ProviderSpec.fromJson(const {
    'id': 'kiro',
    'display_name': 'Kiro',
    'base_url': 'https://q.us-east-1.amazonaws.com',
    'protocols': ['anthropic', 'chat_completions'],
    'auth': 'bearer',
    'credential': 'kiro_refresh',
    'billing': 'subscription',
    'region': 'Global',
    'plan': 'Standard',
  }),
];

/// oauth_refresh(订阅登录态)账号样本。
final accountOAuth = Account.fromJson(const {
  'name': 'gpt-1',
  'provider_id': 'openai.global.subscribe.codex',
  'credential': {
    'kind': 'oauth_refresh',
    'refresh_token': 'rt-a***z',
    'account_id': 'acc-123',
  },
  'base_url': '',
  'enabled': true,
});

final account = Account.fromJson(const {
  'name': 'ds-1',
  'provider_id': 'deepseek.global.api.standard',
  'credential': {'kind': 'api_key', 'api_key': 'sk-d***efgh'},
  'base_url': 'https://ds.example.com',
  'headers': <String, dynamic>{'x-tenant': 'a'},
  'enabled': false,
});

/// 无 base_url 的账号:拷贝/编辑时请求地址应回落到提供商默认值。
final accountNoOverride = Account.fromJson(const {
  'name': 'ds-2',
  'provider_id': 'deepseek.global.api.standard',
  'credential': {'kind': 'api_key', 'api_key': 'sk-x***y'},
  'base_url': '',
  'enabled': true,
});

/// 带额度查询节奏配置的账号:分栏预填与清除语义的样本。
final accountWithQuota = Account.fromJson(const {
  'name': 'ds-3',
  'provider_id': 'deepseek.global.api.standard',
  'credential': {'kind': 'api_key', 'api_key': 'sk-s***t'},
  'base_url': '',
  'enabled': true,
  'quota_settings': {
    'auto_interval_minutes': 5,
    'stop_interval_minutes': 8,
  },
});

/// 关掉实时额度查询的账号:总开关回显与间隔置灰的样本。
final accountQuotaDisabled = Account.fromJson(const {
  'name': 'ds-4',
  'provider_id': 'deepseek.global.api.standard',
  'credential': {'kind': 'api_key', 'api_key': 'sk-s***t'},
  'base_url': '',
  'enabled': true,
  'quota_settings': {'enabled': false, 'auto_interval_minutes': 5},
});

/// kimi 账号样本:api_key 形态附带网页会话 token(月度额度凭据)。
final accountKimi = Account.fromJson(const {
  'name': 'kimi-1',
  'provider_id': 'kimi.global.subscribe.coding',
  'credential': {
    'kind': 'api_key',
    'api_key': 'sk-k***i',
    'web_refresh_token': 'eyJh***xyz',
  },
  'base_url': '',
  'enabled': true,
});

final accountBailian = Account.fromJson(const {
  'name': 'bailian-1',
  'provider_id': 'bailian.cn.subscribe.token-plan',
  'credential': {
    'kind': 'api_key',
    'api_key': 'sk-b***n',
  },
  'enabled': true,
});

ApiClient stubClient() => ApiClient(
      baseUrl: 'http://127.0.0.1:8080',
      adminKey: 'adm',
      httpClient: MockClient((_) async => http.Response('{}', 200)),
    );

/// 记录请求 body 的客户端,用于断言提交时 base_url 的实际取值。
ApiClient recordingClient(List<String> captured) => ApiClient(
      baseUrl: 'http://127.0.0.1:8080',
      adminKey: 'adm',
      httpClient: MockClient((req) async {
        captured.add(req.body);
        return http.Response(
            jsonEncode({
              'account': {
                'name': 'a',
                'provider_id': 'deepseek.global.api.standard',
                'credential': {'kind': 'api_key', 'api_key': 'k'},
                'base_url': '',
                'enabled': true,
              }
            }),
            200);
      }),
    );

/// 表单已是整页路由组件,直接作为 home pump。
Future<void> pumpForm(
  WidgetTester tester, {
  Account? copyFrom,
  Account? editing,
  ApiClient? client,
}) async {
  tester.view.physicalSize = const Size(1200, 900);
  tester.view.devicePixelRatio = 1.0;
  addTearDown(tester.view.reset);
  await tester.pumpWidget(MaterialApp(
    theme: buildAppTheme(),
    home: AccountForm(
        client: client ?? stubClient(),
        providers: providers,
        onDone: (_) {},
        editing: editing,
        copyFrom: copyFrom),
  ));
  await tester.pumpAndSettle();
}

String fieldText(WidgetTester tester, String key) => tester
    .widget<TextFormField>(find.byKey(ValueKey(key)))
    .controller!
    .text;

/// 级联从上至下逐级点选(厂商→计费模式→服务区域→服务类型)。
/// 服务类型级对所有组渲染:Standard 单选项组自动落定且禁用,不传 plan;
/// 只有多类型组才传 plan 做选择。
Future<void> selectCascade(
  WidgetTester tester, {
  required String vendor,
  required String billing,
  required String region,
  String? plan,
}) async {
  Future<void> pick(String key, String option) async {
    final field = find.byKey(ValueKey(key));
    await tester.ensureVisible(field);
    await tester.tap(field);
    await tester.pumpAndSettle();
    await tester.tap(find.text(option).last);
    await tester.pumpAndSettle();
  }

  await pick('account-provider-vendor', vendor);
  await pick('account-provider-billing', billing);
  await pick('account-provider-region', region);
  if (plan != null) {
    await pick('account-provider-plan', plan);
  }
}

void main() {
  final accountKiro = Account.fromJson(const {
    'name': 'kiro-1',
    'provider_id': 'kiro',
    'enabled': true,
    'credential': {
      'kind': 'kiro_refresh',
      'refresh_token': '********',
      'profile_arn': 'arn:profile:existing',
      'region': 'eu-west-1',
      'api_region': 'us-east-1',
      'client_id': 'stored-client',
      'client_secret': '********',
    },
  });

  Directory useKiroTempHome() {
    final home = Directory.systemTemp.createTempSync('msu-kiro-test');
    debugKiroHomeOverride = home.path;
    addTearDown(() {
      debugKiroHomeOverride = null;
      home.deleteSync(recursive: true);
    });
    return home;
  }

  File writeKiroCache(String name, Map<String, dynamic> json) {
    final file = File('${kiroCacheDir()}${Platform.pathSeparator}$name');
    file.parent.createSync(recursive: true);
    file.writeAsStringSync(jsonEncode(json));
    return file;
  }

  Future<void> enterKiroField(WidgetTester tester, String key, String text) async {
    final field = find.byKey(ValueKey(key));
    await tester.ensureVisible(field);
    await tester.enterText(field, text);
  }

  Future<void> clickKiroButton(WidgetTester tester, String key) async {
    final button = find.byKey(ValueKey(key));
    await tester.pumpAndSettle();
    await tester.ensureVisible(button);
    await tester.pumpAndSettle();
    await tester.tap(button);
    await tester.pump();
    await tester.pump(const Duration(seconds: 3));
    await tester.pumpAndSettle();
  }

  test('Kiro account parses redacted fields with compatible defaults', () {
    expect(accountKiro.credentialKind, 'kiro_refresh');
    expect(accountKiro.maskedRefreshToken, '********');
    expect(accountKiro.profileArn, 'arn:profile:existing');
    expect(accountKiro.region, 'eu-west-1');
    expect(accountKiro.apiRegion, 'us-east-1');
    expect(accountKiro.clientId, 'stored-client');
    expect(accountKiro.maskedClientSecret, '********');
    final minimal = Account.fromJson({'name': 'minimal'});
    expect(minimal.region, 'us-east-1');
    expect(minimal.profileArn, isEmpty);
    expect(minimal.clientId, isEmpty);
    expect(minimal.maskedClientSecret, isEmpty);
    expect(account.region, 'us-east-1');
  });

  test('Kiro parser supports Desktop and SSO camelCase credentials', () {
    final desktop = parseKiroCredentialJson('{"refreshToken":" desktop-rt "}');
    expect(desktop['kind'], 'kiro_refresh');
    expect(desktop['refresh_token'], 'desktop-rt');
    expect(desktop['profile_arn'], isEmpty);
    expect(desktop['region'], 'us-east-1');
    expect(desktop['client_id'], isEmpty);
    final sso = parseKiroCredentialJson(jsonEncode({
      'refreshToken': 'sso-rt',
      'profileArn': 'arn:profile:new',
      'region': 'eu-central-1',
      'apiRegion': 'us-east-1',
      'clientId': 'sso-client',
      'clientSecret': 'sso-secret',
      'accessToken': 'initial-access',
      'expiresAt': '2030-01-01T02:00:00+02:00',
    }));
    expect(sso['profile_arn'], 'arn:profile:new');
    expect(sso['region'], 'eu-central-1');
    expect(sso['api_region'], 'us-east-1');
    expect(sso['client_id'], 'sso-client');
    expect(sso['client_secret'], 'sso-secret');
    expect(sso['access_token'], 'initial-access');
    expect(sso['expiry'], '2030-01-01T00:00:00.000Z');
  });

  test('Kiro parser rejects invalid JSON, missing refresh and incomplete SSO safely', () {
    for (final text in [
      'sensitive-invalid-json',
      '[{"refreshToken":"sensitive-token"}]',
      '{"accessToken":"sensitive-token"}',
      '{"refreshToken":" "}',
      '{"refreshToken":123}',
      '{"refreshToken":"********"}',
      '{"refreshToken":"bad token"}',
      '{"refreshToken":"rt","clientId":"client"}',
      '{"refreshToken":"rt","clientSecret":"sensitive-secret"}',
      '{"refreshToken":"rt","expiresAt":"sensitive-date"}',
      '{"refreshToken":"rt","profileArn":[]}',
    ]) {
      expect(() => parseKiroCredentialJson(text),
          throwsA(isA<FormatException>().having(
              (e) => e.toString(), 'sanitized error', isNot(contains('sensitive')))));
    }
  });

  for (final sso in [false, true]) {
    testWidgets('Kiro create submits ${sso ? 'SSO' : 'Desktop'} credential',
        (tester) async {
      final captured = <String>[];
      await pumpForm(tester, client: recordingClient(captured));
      await selectCascade(tester, vendor: 'Kiro', billing: '订阅', region: '全球');
      expect(find.byKey(const ValueKey('account-api-key')), findsNothing);
      expect(find.byKey(const ValueKey('account-account-id')), findsNothing);
      expect(find.byKey(const ValueKey('oauth-autofill-cli')), findsNothing);
      expect(find.byKey(const ValueKey('oauth-autofill-app')), findsNothing);
      expect(fieldText(tester, 'account-auth-region'), 'us-east-1');
      await enterKiroField(tester, 'account-name', 'kiro-new');
      await enterKiroField(tester, 'account-refresh-token', ' rt-new ');
      if (sso) {
        await enterKiroField(tester, 'account-client-id', ' client-new ');
        await enterKiroField(tester, 'account-client-secret', ' secret-new ');
      }
      await tester.ensureVisible(find.widgetWithText(FilledButton, '创建'));
      await tester.tap(find.widgetWithText(FilledButton, '创建'));
      await tester.pumpAndSettle();
      final body = jsonDecode(captured.single) as Map<String, dynamic>;
      expect(body['provider_id'], 'kiro');
      expect(body.containsKey('api_key'), isFalse);
      expect(body['credential'], {
        'kind': 'kiro_refresh',
        'refresh_token': 'rt-new',
        'profile_arn': '',
        'region': 'us-east-1',
        'api_region': '',
        'client_id': sso ? 'client-new' : '',
        'client_secret': sso ? 'secret-new' : '',
      });
    });
  }

  for (final missing in ['refresh', 'client-id', 'client-secret', 'masked-refresh']) {
    testWidgets('Kiro create validates $missing', (tester) async {
      final captured = <String>[];
      await pumpForm(tester, copyFrom: accountKiro, client: recordingClient(captured));
      await enterKiroField(tester, 'account-client-id', '');
      if (missing != 'refresh') {
        await enterKiroField(tester, 'account-refresh-token',
            missing == 'masked-refresh' ? '********' : 'rt');
      }
      if (missing == 'client-id') {
        await enterKiroField(tester, 'account-client-secret', 'secret');
      } else if (missing == 'client-secret') {
        await enterKiroField(tester, 'account-client-id', 'client');
      }
      await tester.ensureVisible(find.widgetWithText(FilledButton, '创建'));
      await tester.tap(find.widgetWithText(FilledButton, '创建'));
      await tester.pumpAndSettle();
      expect(captured, isEmpty);
      expect(find.text(missing == 'refresh'
          ? 'Refresh Token 不能为空'
          : missing == 'masked-refresh'
              ? '请填写有效凭据,不能使用脱敏星号'
              : 'SSO Client ID 与 Client Secret 必须一起填写'), findsOneWidget);
    });
  }

  testWidgets('Kiro edit secrets stay blank and pure-starred with independent eyes',
      (tester) async {
    await pumpForm(tester, editing: accountKiro);
    final detailsToggle = find.byKey(const ValueKey('kiro-details-toggle'));
    await tester.ensureVisible(detailsToggle);
    await tester.tap(detailsToggle);
    await tester.pumpAndSettle();
    expect(fieldText(tester, 'account-profile-arn'), 'arn:profile:existing');
    expect(fieldText(tester, 'account-auth-region'), 'eu-west-1');
    expect(fieldText(tester, 'account-api-region'), 'us-east-1');
    expect(fieldText(tester, 'account-client-id'), 'stored-client');
    for (final key in ['account-refresh-token', 'account-client-secret']) {
      final finder = find.byKey(ValueKey(key));
      final input = find.descendant(of: finder, matching: find.byType(TextField));
      var field = tester.widget<TextField>(input);
      expect(field.controller!.text, isEmpty);
      expect(field.obscureText, isTrue);
      expect(field.decoration!.hintText, '************');
      await tester.ensureVisible(finder);
      await tester.tap(find.descendant(of: finder, matching: find.byTooltip('显示')));
      await tester.pump();
      field = tester.widget<TextField>(input);
      expect(field.obscureText, isFalse);
      expect(field.decoration!.hintText, '************');
      expect(field.controller!.text, isEmpty);
    }
  });

  testWidgets('Kiro login card checklist shows import contents when empty',
      (tester) async {
    await pumpForm(tester);
    await selectCascade(tester, vendor: 'Kiro', billing: '订阅', region: '全球');
    expect(find.text('Kiro 登录态'), findsOneWidget);
    expect(find.text('未配置'), findsOneWidget);
    // Refresh Token / SSO 凭据 / Profile ARN 待导入;区域默认 us-east-1 已算配置。
    expect(find.text('待导入'), findsNWidgets(3));
    // 清单值与折叠组内默认值的输入框都会显示该文案。
    expect(find.text('us-east-1'), findsWidgets);
    expect(find.text('从 Kiro App 导入'), findsOneWidget);
    expect(find.text('凭据详情(导入自动填充,一般无需修改)'), findsOneWidget);
  });

  testWidgets('Kiro login card checklist reflects stored credentials in edit',
      (tester) async {
    await pumpForm(tester, editing: accountKiro);
    // 徽标 + Refresh Token + SSO 凭据三处「已配置」。
    expect(find.text('已配置'), findsNWidgets(3));
    expect(find.text('待导入'), findsNothing);
    expect(find.text('…ting'), findsOneWidget);
    expect(find.text('eu-west-1 / us-east-1'), findsOneWidget);
    expect(find.text('从 Kiro App 重新导入'), findsOneWidget);
  });

  for (final change in ['none', 'region', 'profile', 'client', 'secret', 'refresh']) {
    testWidgets('Kiro edit submits parameter changes and preserves blank secrets: $change',
        (tester) async {
      final captured = <String>[];
      await pumpForm(tester, editing: accountKiro, client: recordingClient(captured));
      final changes = {
        'region': ('account-auth-region', 'ap-southeast-1'),
        'profile': ('account-profile-arn', 'arn:profile:changed'),
        'client': ('account-client-id', 'changed-client'),
        'secret': ('account-client-secret', 'changed-secret'),
        'refresh': ('account-refresh-token', 'changed-refresh'),
      };
      if (changes.containsKey(change)) {
        final (key, text) = changes[change]!;
        await enterKiroField(tester, key, text);
      }
      if (change == 'client') {
        await enterKiroField(tester, 'account-client-secret', 'new-client-secret');
      }
      await enterKiroField(tester, 'account-api-region', 'eu-central-1');
      await tester.ensureVisible(find.widgetWithText(FilledButton, '保存'));
      await tester.tap(find.widgetWithText(FilledButton, '保存'));
      await tester.pumpAndSettle();
      final body = jsonDecode(captured.single) as Map<String, dynamic>;
      expect(body['credential'], {
        'kind': 'kiro_refresh',
        'refresh_token': change == 'refresh' ? 'changed-refresh' : '',
        'profile_arn': change == 'profile' ? 'arn:profile:changed' : 'arn:profile:existing',
        'region': change == 'region' ? 'ap-southeast-1' : 'eu-west-1',
        'api_region': 'eu-central-1',
        'client_id': change == 'client' ? 'changed-client' : 'stored-client',
        'client_secret': change == 'secret'
            ? 'changed-secret'
            : change == 'client' ? 'new-client-secret' : '',
      });
      expect(body['credential'].toString(), isNot(contains('***')));
    });
  }

  for (final clearClient in [false, true]) {
    testWidgets('Kiro edit requires a new secret unless switching to Desktop: $clearClient',
        (tester) async {
      final captured = <String>[];
      await pumpForm(tester, editing: accountKiro, client: recordingClient(captured));
      await enterKiroField(tester, 'account-client-id', clearClient ? '' : 'new-client');
      await tester.ensureVisible(find.widgetWithText(FilledButton, '保存'));
      await tester.tap(find.widgetWithText(FilledButton, '保存'));
      await tester.pumpAndSettle();
      if (clearClient) {
        final credential = jsonDecode(captured.single)['credential'];
        expect(credential['client_id'], isEmpty);
        expect(credential['client_secret'], isEmpty);
      } else {
        expect(captured, isEmpty);
        expect(find.text('SSO Client ID 与 Client Secret 必须一起填写'), findsOneWidget);
      }
    });
  }

  for (final field in ['account-refresh-token', 'account-auth-region',
    'account-client-id', 'account-client-secret']) {
    testWidgets('Kiro imported access token is discarded after changing $field',
        (tester) async {
      useKiroTempHome();
      writeKiroCache('kiro-auth-token.json', {
        'refreshToken': 'import-refresh', 'clientId': 'import-client',
        'clientSecret': 'import-secret', 'accessToken': 'import-access',
        'expiresAt': '2030-01-01T00:00:00Z',
      });
      final captured = <String>[];
      await pumpForm(tester, editing: accountKiro, client: recordingClient(captured));
      await clickKiroButton(tester, 'kiro-autofill-app');
      await enterKiroField(tester, field,
          field == 'account-auth-region' ? 'eu-west-1' : 'changed');
      await tester.ensureVisible(find.widgetWithText(FilledButton, '保存'));
      await tester.tap(find.widgetWithText(FilledButton, '保存'));
      await tester.pumpAndSettle();
      final credential = jsonDecode(captured.single)['credential'] as Map;
      expect(credential.containsKey('access_token'), isFalse);
      expect(credential.containsKey('expiry'), isFalse);
    });
  }

  test('Kiro local Desktop import reads only fixed cache and never writes', () {
    final home = useKiroTempHome();
    expect(kiroCacheDir(), [home.path, '.aws', 'sso', 'cache'].join(Platform.pathSeparator));
    final file = writeKiroCache('kiro-auth-token.json', {'refreshToken': 'desktop-rt'});
    final before = file.readAsStringSync();
    writeKiroCache('unrelated.json', {'refreshToken': 'must-not-scan'});
    final credential = loadKiroAppCredentials();
    expect(credential['refresh_token'], 'desktop-rt');
    expect(credential['region'], 'us-east-1');
    expect(file.readAsStringSync(), before);
    expect(debugCodexHomeOverride, isNull);
  });

  test('Kiro local SSO import matches hash registration with direct fields taking priority', () {
    useKiroTempHome();
    final token = writeKiroCache('kiro-auth-token.json', {
      'refreshToken': 'sso-rt', 'clientIdHash': 'abc123', 'region': 'eu-west-1',
    });
    final registration = writeKiroCache('abc123.json', {
      'clientId': 'registered-client', 'clientSecret': 'registered-secret',
    });
    writeKiroCache('other.json', {'clientId': 'wrong', 'clientSecret': 'wrong'});
    final before = registration.readAsStringSync();
    expect(loadKiroAppCredentials()['client_id'], 'registered-client');
    expect(loadKiroAppCredentials()['client_secret'], 'registered-secret');
    expect(loadKiroAppCredentials()['region'], 'eu-west-1');
    token.writeAsStringSync(jsonEncode({
      'refreshToken': 'sso-rt', 'clientIdHash': 'abc123',
      'clientId': 'direct-client', 'clientSecret': 'direct-secret',
    }));
    expect(loadKiroAppCredentials()['client_id'], 'direct-client');
    expect(loadKiroAppCredentials()['client_secret'], 'direct-secret');
    expect(registration.readAsStringSync(), before);
  });

  test('Kiro local import rejects unsafe hashes without reading external files', () {
    final home = useKiroTempHome();
    final outside = File('${home.path}${Platform.pathSeparator}outside.json')
      ..writeAsStringSync('{"clientId":"outside","clientSecret":"outside-secret"}');
    for (final hash in <dynamic>[
      '../../outside', r'..\..\outside', '/tmp/outside', r'C:\outside',
      'abc/def', r'abc\def', '.', '', '%2e%2e', 12, 'a' * 129,
    ]) {
      final text = jsonEncode({'refreshToken': 'rt', 'clientIdHash': hash});
      expect(() => importKiroCredentialJson(text), throwsA(isA<FormatException>()));
      writeKiroCache('kiro-auth-token.json', {'refreshToken': 'rt', 'clientIdHash': hash});
      expect(() => loadKiroAppCredentials(), throwsA(isA<FormatException>().having(
          (e) => e.toString(), 'no external content', isNot(contains('outside-secret')))));
    }
    expect(outside.readAsStringSync(), contains('outside-secret'));
  });

  test('Kiro local import rejects symlink escaping the cache', () {
    final home = useKiroTempHome();
    final outside = File('${home.path}${Platform.pathSeparator}outside.json')
      ..writeAsStringSync('{"clientId":"external","clientSecret":"external-secret"}');
    writeKiroCache('kiro-auth-token.json', {'refreshToken': 'rt', 'clientIdHash': 'abc123'});
    try {
      Link('${kiroCacheDir()}${Platform.pathSeparator}abc123.json').createSync(outside.path);
    } on FileSystemException {
      markTestSkipped('当前系统不允许创建符号链接');
      return;
    }
    expect(() => loadKiroAppCredentials(), throwsA(isA<FormatException>()));
  });

  test('Kiro local import fails closed on missing or invalid registration', () {
    useKiroTempHome();
    writeKiroCache('kiro-auth-token.json', {'refreshToken': 'rt', 'clientIdHash': 'abc123'});
    expect(() => loadKiroAppCredentials(), throwsA(isA<FileSystemException>()));
    writeKiroCache('abc123.json', {'clientId': 'client'});
    expect(() => loadKiroAppCredentials(), throwsA(isA<FormatException>()));
  });

  testWidgets('Kiro App button imports temporary SSO cache and submits initial tokens',
      (tester) async {
    useKiroTempHome();
    final captured = <String>[];
    final file = writeKiroCache('kiro-auth-token.json', {
      'refreshToken': 'local-refresh', 'clientIdHash': 'abc123',
      'profileArn': 'arn:local:profile', 'region': 'eu-west-1',
      'apiRegion': 'us-east-1', 'accessToken': 'local-access',
      'expiresAt': '2030-01-01T00:00:00Z',
    });
    final before = file.readAsStringSync();
    writeKiroCache('abc123.json', {'clientId': 'local-client', 'clientSecret': 'local-secret'});
    await pumpForm(tester, editing: accountKiro, client: recordingClient(captured));
    await clickKiroButton(tester, 'kiro-autofill-app');
    expect(fieldText(tester, 'account-refresh-token'), 'local-refresh');
    expect(fieldText(tester, 'account-profile-arn'), 'arn:local:profile');
    expect(fieldText(tester, 'account-client-secret'), 'local-secret');
    expect(file.readAsStringSync(), before);
    expect(find.textContaining('local-access'), findsNothing);
    await tester.ensureVisible(find.widgetWithText(FilledButton, '保存'));
    await tester.tap(find.widgetWithText(FilledButton, '保存'));
    await tester.pumpAndSettle();
    final body = jsonDecode(captured.single) as Map<String, dynamic>;
    expect(body['credential']['access_token'], 'local-access');
    expect(body['credential']['expiry'], '2030-01-01T00:00:00.000Z');
    expect(body['credential']['client_id'], 'local-client');
  });

  testWidgets('Kiro missing App cache is sanitized and does not scan other files',
      (tester) async {
    final home = useKiroTempHome();
    writeKiroCache('other-token.json', {'refreshToken': 'must-not-scan'});
    await pumpForm(tester, editing: accountKiro);
    final button = find.byKey(const ValueKey('kiro-autofill-app'));
    await tester.ensureVisible(button);
    await tester.tap(button);
    await tester.pump();
    expect(find.text('无法读取 Kiro App 缓存,请先登录 Kiro App'), findsOneWidget);
    expect(find.textContaining(home.path), findsNothing);
    expect(fieldText(tester, 'account-refresh-token'), isEmpty);
    await tester.pump(const Duration(seconds: 3));
    await tester.pumpAndSettle();
  });

  test('bailian Token Plan provider declares quota queryable', () {
    expect(providers.firstWhere((p) => p.id == 'bailian.cn.subscribe.token-plan').quotaQueryable,
        isTrue);
  });

  testWidgets('bailian create payload carries only the API key', (tester) async {
    final captured = <String>[];
    await pumpForm(tester, client: recordingClient(captured));
    await selectCascade(tester,
        vendor: '百炼', billing: '订阅', region: '中国', plan: 'Token Plan');
    await tester.enterText(find.byKey(const ValueKey('account-name')), 'bl-new');
    await tester.enterText(
        find.byKey(const ValueKey('account-api-key')), ' sk-create ');
    await tester.ensureVisible(find.widgetWithText(FilledButton, '创建'));
    await tester.tap(find.widgetWithText(FilledButton, '创建'));
    await tester.pumpAndSettle();
    final body = jsonDecode(captured.single) as Map<String, dynamic>;
    expect(body['provider_id'], 'bailian.cn.subscribe.token-plan');
    expect(body.containsKey('api_key'), isFalse);
    expect(body['credential'], {
      'kind': 'api_key',
      'api_key': 'sk-create',
    });
  });

  testWidgets('bailian edit payload leaves blank API key for backend merge',
      (tester) async {
    final captured = <String>[];
    await pumpForm(tester,
        editing: accountBailian, client: recordingClient(captured));
    await tester.ensureVisible(find.widgetWithText(FilledButton, '保存'));
    await tester.tap(find.widgetWithText(FilledButton, '保存'));
    await tester.pumpAndSettle();
    final body = jsonDecode(captured.single) as Map<String, dynamic>;
    expect(body['credential'], {
      'kind': 'api_key',
      'api_key': '',
    }, reason: '编辑时留空按字段保留,不会把掩码提交给后端');
  });

  final whereCmd = Platform.isWindows ? 'where' : 'which';

  test('bl ensure+login installs missing CLI then validates AK/SK', () async {
    final calls = <String>[];
    debugBlExecOverride = (exe, args) async {
      calls.add([exe, ...args].join(' '));
      final missing = exe == whereCmd && args.first == 'bl';
      return ProcessResult(1, missing ? 1 : 0, '', '');
    };
    addTearDown(() => debugBlExecOverride = null);
    final error = await ensureBlAndLogin('ak-id', 'ak-secret');
    expect(error, isNull);
    expect(calls, [
      '$whereCmd bl',
      '$whereCmd npm',
      'npm install -g bailian-cli',
      'bl auth login --open-api --access-key-id ak-id'
          ' --access-key-secret ak-secret',
      'bl auth generate-access-token',
    ]);
  });

  test('bl ensure+login skips install when CLI already present', () async {
    final calls = <String>[];
    debugBlExecOverride = (exe, args) async {
      calls.add([exe, ...args].join(' '));
      return ProcessResult(1, 0, '', '');
    };
    addTearDown(() => debugBlExecOverride = null);
    expect(await ensureBlAndLogin('ak-id', 'ak-secret'), isNull);
    expect(calls, hasLength(3));
    expect(calls.first, '$whereCmd bl');
    expect(calls.any((c) => c.startsWith('npm ')), isFalse);
  });

  test('bl ensure+login reports missing Node.js when neither tool exists',
      () async {
    debugBlExecOverride =
        (exe, args) async => ProcessResult(1, 1, '', 'not found');
    addTearDown(() => debugBlExecOverride = null);
    final error = await ensureBlAndLogin('ak-id', 'ak-secret');
    expect(error, contains('Node.js'));
  });

  test('bl ensure+login sanitizes AK/SK out of failure output', () async {
    debugBlExecOverride = (exe, args) async {
      if (exe == 'bl') {
        return ProcessResult(1, 1, '', 'invalid ak-id / ak-secret pair');
      }
      return ProcessResult(1, 0, '', '');
    };
    addTearDown(() => debugBlExecOverride = null);
    final error = await ensureBlAndLogin('ak-id', 'ak-secret');
    expect(error, contains('AccessKey 未通过 bl 验证'));
    expect(error, isNot(contains('ak-id')));
    expect(error, isNot(contains('ak-secret')));
    expect(error, contains('***'));
  });

  test('bl ensure+login surfaces token mint failure after successful login',
      () async {
    debugBlExecOverride = (exe, args) async {
      final mint = exe == 'bl' && args.contains('generate-access-token');
      return ProcessResult(1, mint ? 1 : 0, '', mint ? 'pop denied' : '');
    };
    addTearDown(() => debugBlExecOverride = null);
    final error = await ensureBlAndLogin('ak-id', 'ak-secret');
    expect(error, contains('签发额度 token 失败'));
    expect(error, contains('pop denied'));
  });

  Future<void> expandQuotaSection(WidgetTester tester) async {
    await tester.ensureVisible(find.text('额度查询'));
    await tester.tap(find.text('额度查询'));
    await tester.pumpAndSettle();
  }

  testWidgets('bailian quota section shows AK fields and install button',
      (tester) async {
    await pumpForm(tester, editing: accountBailian);
    await expandQuotaSection(tester);
    final secretFinder = find.byKey(const ValueKey('bailian-access-key-secret'));
    expect(find.byKey(const ValueKey('bailian-access-key-id')), findsOneWidget);
    expect(secretFinder, findsOneWidget);
    expect(find.byKey(const ValueKey('bailian-bl-install')), findsOneWidget);
    final input =
        find.descendant(of: secretFinder, matching: find.byType(TextField));
    expect(tester.widget<TextField>(input).obscureText, isTrue,
        reason: 'Secret 默认纯星号,眼睛才亮');
  });

  testWidgets('bailian AK fields are absent for other providers',
      (tester) async {
    for (final editing in [account, accountKimi]) {
      await pumpForm(tester, editing: editing);
      await expandQuotaSection(tester);
      expect(find.byKey(const ValueKey('bailian-access-key-id')), findsNothing);
      expect(
          find.byKey(const ValueKey('bailian-access-key-secret')), findsNothing);
      expect(find.byKey(const ValueKey('bailian-bl-install')), findsNothing);
      await tester.pumpWidget(const SizedBox.shrink());
    }
  });

  testWidgets('bl button requires both AK fields before running',
      (tester) async {
    var ran = false;
    debugBlExecOverride = (exe, args) async {
      ran = true;
      return ProcessResult(1, 0, '', '');
    };
    addTearDown(() => debugBlExecOverride = null);
    await pumpForm(tester, editing: accountBailian);
    await expandQuotaSection(tester);
    final button = find.byKey(const ValueKey('bailian-bl-install'));
    await tester.ensureVisible(button);
    await tester.tap(button);
    await tester.pump();
    expect(ran, isFalse);
    expect(find.text('请先填入 AccessKey ID 与 Secret'), findsOneWidget);
    await tester.pump(const Duration(seconds: 3));
    await tester.pumpAndSettle();
  });

  testWidgets('bl button validates filled keys and reports success',
      (tester) async {
    debugBlExecOverride =
        (exe, args) async => ProcessResult(1, 0, '', '');
    addTearDown(() => debugBlExecOverride = null);
    await pumpForm(tester, editing: accountBailian);
    await expandQuotaSection(tester);
    await tester.enterText(
        find.byKey(const ValueKey('bailian-access-key-id')), 'ak-id');
    await tester.enterText(
        find.byKey(const ValueKey('bailian-access-key-secret')), 'ak-secret');
    final button = find.byKey(const ValueKey('bailian-bl-install'));
    await tester.ensureVisible(button);
    await tester.tap(button);
    await tester.pump();
    await tester.pump();
    expect(find.textContaining('AccessKey 验证通过'), findsOneWidget);
    await tester.pump(const Duration(seconds: 3));
    await tester.pumpAndSettle();
  });

  testWidgets('copy prefills config but keeps create semantics', (tester) async {
    await pumpForm(tester, copyFrom: account);

    expect(find.text('拷贝 ds-1'), findsOneWidget);
    expect(find.text('DeepSeek'), findsNWidgets(2),
        reason: '厂商级跟随来源账号反推预填(下拉选中值+分栏副标题各一处)');
    expect(find.text('按量计费'), findsOneWidget, reason: '计费模式级预填');
    expect(find.text('全球'), findsOneWidget, reason: '服务区域级预填');
    expect(fieldText(tester, 'account-name'), 'ds-1-copy');
    expect(fieldText(tester, 'account-base-url'), 'https://ds.example.com',
        reason: '已存覆盖值直接预填');
    expect(find.textContaining('************'), findsOneWidget,
        reason: '默认纯星号,不泄露掩码里的任何字符');
    expect(find.byType(SwitchListTile), findsNothing,
        reason: '启停由列表行开关控制,表单不再展示');
    expect(find.text('创建'), findsOneWidget,
        reason: 'copy is a create, not an edit');
  });

  testWidgets('basic info section defaults expanded with config subtitle',
      (tester) async {
    await pumpForm(tester, editing: account);

    expect(find.text('基本信息'), findsOneWidget);
    expect(find.text('DeepSeek'), findsWidgets,
        reason: '副标题与厂商下拉选中值都只留厂商名');
    expect(find.text('DeepSeek · https://ds.example.com'), findsNothing,
        reason: '基本信息副标题不再拼接具体访问地址');
    expect(find.byKey(const ValueKey('account-api-key')), findsOneWidget,
        reason: '主信息默认展开,字段直接可见');
    expect(fieldText(tester, 'account-name'), 'ds-1');
    expect(
        tester
            .widget<TextFormField>(find.byKey(const ValueKey('account-name')))
            .enabled,
        isFalse,
        reason: '编辑态账号名灰框禁用展示,资源键不可改');

    // 折叠再展开,已填内容不丢(分栏只裁剪不卸载)。
    await tester.tap(find.text('基本信息'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('基本信息'));
    await tester.pumpAndSettle();
    expect(fieldText(tester, 'account-base-url'), 'https://ds.example.com');
  });

  testWidgets('disabled name field border matches enabled field style',
      (tester) async {
    await pumpForm(tester, editing: account);

    InputDecoration decoOf(String key) => tester
        .widget<InputDecorator>(find
            .descendant(
                of: find.byKey(ValueKey(key)),
                matching: find.byType(InputDecorator))
            .first)
        .decoration;

    final name = decoOf('account-name');
    final url = decoOf('account-base-url');
    final disabled = name.disabledBorder as OutlineInputBorder;
    final enabled = url.enabledBorder as OutlineInputBorder;
    expect(disabled.borderSide, enabled.borderSide,
        reason: '禁用框描边颜色/宽度与启用框一致');
    expect(disabled.borderRadius, enabled.borderRadius, reason: '圆角一致');
    expect(name.fillColor, url.fillColor, reason: '填充色一致');
  });

  testWidgets('edit masks key hint behind asterisks until eye tapped',
      (tester) async {
    await pumpForm(tester, editing: account);

    expect(find.textContaining('************'), findsOneWidget);
    expect(find.textContaining('sk-d***efgh'), findsNothing,
        reason: '未点眼睛前掩码字符一个都不露');

    await tester.ensureVisible(find.byTooltip('显示'));
    await tester.tap(find.byTooltip('显示'));
    await tester.pump();
    expect(find.textContaining('sk-d***efgh'), findsOneWidget);
    expect(find.textContaining('************'), findsNothing);

    await tester.tap(find.byTooltip('隐藏'));
    await tester.pump();
    expect(find.textContaining('************'), findsOneWidget);
  });

  testWidgets('copy falls back to provider default when no override',
      (tester) async {
    await pumpForm(tester, copyFrom: accountNoOverride);
    expect(fieldText(tester, 'account-base-url'), 'https://api.deepseek.com');
  });

  testWidgets('plain create stays blank until cascade selected', (tester) async {
    await pumpForm(tester);
    expect(find.text('新建账号'), findsOneWidget);
    expect(fieldText(tester, 'account-name'), isEmpty);
    expect(fieldText(tester, 'account-base-url'), isEmpty,
        reason: '三级级联未选定前没有提供商,地址留空');

    await selectCascade(tester, vendor: 'DeepSeek', billing: '按量计费', region: '全球');
    expect(fieldText(tester, 'account-base-url'), 'https://api.deepseek.com',
        reason: '三级选定后自动带出该提供商的默认请求地址');
  });

  testWidgets('standard-only group shows disabled plan level auto-settled',
      (tester) async {
    await pumpForm(tester);
    await selectCascade(tester, vendor: 'DeepSeek', billing: '按量计费', region: '全球');
    final plan = find.byKey(const ValueKey('account-provider-plan'));
    expect(plan, findsOneWidget,
        reason: '服务类型级对所有组渲染,Standard 组也不例外');
    expect(
        tester.widget<StyledDropdown>(
            find.descendant(of: plan, matching: find.byType(StyledDropdown))).enabled,
        isFalse,
        reason: '单选项组无需选择:自动落定、下拉禁用');
    expect(find.text('标准'), findsWidgets,
        reason: 'Standard 占位标签显示为"标准"');
    expect(fieldText(tester, 'account-base-url'), 'https://api.deepseek.com',
        reason: '自动落定不影响解析:三级选定即带出该提供商的默认地址');
  });

  testWidgets('multi-plan group resolves provider only after plan chosen',
      (tester) async {
    await pumpForm(tester);
    await selectCascade(tester, vendor: '百炼', billing: '订阅', region: '中国');
    expect(find.byKey(const ValueKey('account-provider-plan')), findsOneWidget,
        reason: '百炼订阅中国区分 Token/Coding Plan 两个服务类型,该级出现');
    expect(fieldText(tester, 'account-base-url'), isEmpty,
        reason: '服务类型未定前提供商不解析,地址留空');

    await tester.tap(find.byKey(const ValueKey('account-provider-plan')));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Coding Plan').last);
    await tester.pumpAndSettle();
    expect(fieldText(tester, 'account-base-url'),
        'https://coding-plan.example.com/v1',
        reason: '选定服务类型后带出该类型端点');
  });

  testWidgets('multi-plan create payload uses the chosen plan provider',
      (tester) async {
    final captured = <String>[];
    await pumpForm(tester, client: recordingClient(captured));
    await selectCascade(tester,
        vendor: '百炼', billing: '订阅', region: '中国', plan: 'Coding Plan');
    await tester.enterText(find.byKey(const ValueKey('account-name')), 'bl-coding');
    await tester.enterText(
        find.byKey(const ValueKey('account-api-key')), 'sk-coding');
    await tester.ensureVisible(find.widgetWithText(FilledButton, '创建'));
    await tester.tap(find.widgetWithText(FilledButton, '创建'));
    await tester.pumpAndSettle();
    final body = jsonDecode(captured.single) as Map<String, dynamic>;
    expect(body['provider_id'], 'bailian.cn.subscribe.coding-plan');
  });

  testWidgets('lower cascade levels stay disabled until upper chosen',
      (tester) async {
    await pumpForm(tester);

    await tester.tap(find.byKey(const ValueKey('account-provider-billing')));
    await tester.pumpAndSettle();
    expect(find.text('按量计费'), findsNothing,
        reason: '厂商未选,计费模式禁用点开不出菜单');

    await tester.tap(find.byKey(const ValueKey('account-provider-vendor')));
    await tester.pumpAndSettle();
    await tester.tap(find.text('DeepSeek').last);
    await tester.pumpAndSettle();

    await tester.tap(find.byKey(const ValueKey('account-provider-region')));
    await tester.pumpAndSettle();
    expect(find.text('全球'), findsNothing,
        reason: '计费模式未选,服务区域禁用点开不出菜单');
  });

  testWidgets('switching vendor resets lower levels and followed url',
      (tester) async {
    await pumpForm(tester);
    await selectCascade(tester, vendor: 'DeepSeek', billing: '按量计费', region: '全球');
    expect(fieldText(tester, 'account-base-url'), 'https://api.deepseek.com');

    await tester.tap(find.byKey(const ValueKey('account-provider-vendor')));
    await tester.pumpAndSettle();
    await tester.tap(find.text('OpenAI').last);
    await tester.pumpAndSettle();

    expect(find.text('按量计费'), findsNothing, reason: '换厂商后计费模式被重置');
    expect(find.text('全球'), findsNothing, reason: '换厂商后服务区域被重置');
    expect(fieldText(tester, 'account-base-url'), isEmpty,
        reason: '地址仍跟随默认值,提供商未解析时清空');
  });

  testWidgets('switching provider swaps an untouched default', (tester) async {
    await pumpForm(tester);

    await selectCascade(tester, vendor: 'OpenAI', billing: '按量计费', region: '全球');

    expect(fieldText(tester, 'account-base-url'), 'https://api.openai.com',
        reason: '用户没改过地址,选定后跟着带出默认值');
  });

  testWidgets('switching provider preserves a customized url', (tester) async {
    await pumpForm(tester);

    await tester.enterText(
        find.byKey(const ValueKey('account-base-url')),
        'https://my-gateway.example.com');
    await tester.pumpAndSettle();

    await selectCascade(tester, vendor: 'OpenAI', billing: '按量计费', region: '全球');

    expect(fieldText(tester, 'account-base-url'),
        'https://my-gateway.example.com');
  });

  testWidgets('submit sends empty override when url equals provider default',
      (tester) async {
    final captured = <String>[];
    await pumpForm(tester, client: recordingClient(captured));

    await selectCascade(tester, vendor: 'DeepSeek', billing: '按量计费', region: '全球');
    await tester.enterText(find.byKey(const ValueKey('account-name')), 'ds-new');
    await tester.enterText(
        find.byKey(const ValueKey('account-api-key')), 'sk-test');
    await tester.tap(find.widgetWithText(FilledButton, '创建'));
    await tester.pumpAndSettle();

    expect(captured, hasLength(1));
    final body = jsonDecode(captured.single) as Map<String, dynamic>;
    expect(body['provider_id'], 'deepseek.global.api.standard', reason: '三级级联最终解析回 provider id');
    expect(body['base_url'], '',
        reason: '与提供商默认值相同→空覆盖,继续跟随提供商');
  });

  testWidgets('submit sends the customized url as override', (tester) async {
    final captured = <String>[];
    await pumpForm(tester, client: recordingClient(captured));

    await selectCascade(tester, vendor: 'DeepSeek', billing: '按量计费', region: '全球');
    await tester.enterText(find.byKey(const ValueKey('account-name')), 'ds-new');
    await tester.enterText(
        find.byKey(const ValueKey('account-api-key')), 'sk-test');
    await tester.enterText(find.byKey(const ValueKey('account-base-url')),
        'https://my-gateway.example.com');
    await tester.tap(find.widgetWithText(FilledButton, '创建'));
    await tester.pumpAndSettle();

    expect(captured, hasLength(1));
    final body = jsonDecode(captured.single) as Map<String, dynamic>;
    expect(body['base_url'], 'https://my-gateway.example.com');
  });

  testWidgets('url without scheme fails validation', (tester) async {
    final captured = <String>[];
    await pumpForm(tester, client: recordingClient(captured));

    await selectCascade(tester, vendor: 'DeepSeek', billing: '按量计费', region: '全球');
    await tester.enterText(find.byKey(const ValueKey('account-name')), 'ds-new');
    await tester.enterText(
        find.byKey(const ValueKey('account-api-key')), 'sk-test');
    await tester.enterText(find.byKey(const ValueKey('account-base-url')),
        'api.deepseek.com');
    await tester.tap(find.widgetWithText(FilledButton, '创建'));
    await tester.pumpAndSettle();

    expect(captured, isEmpty, reason: '校验失败不发请求');
    expect(find.text('需以 http:// 或 https:// 开头'), findsOneWidget);
  });

  testWidgets('copy prefills quota settings and create submits them',
      (tester) async {
    final captured = <String>[];
    await pumpForm(tester,
        copyFrom: accountWithQuota, client: recordingClient(captured));

    // 已有配置让分栏初始展开,两个分钟间隔直接回显
    expect(fieldText(tester, 'quota-interval'), '5');
    expect(fieldText(tester, 'quota-stop-interval'), '8');

    await tester.ensureVisible(find.byKey(const ValueKey('account-name')));
    await tester.enterText(find.byKey(const ValueKey('account-name')), 'ds-new');
    await tester.enterText(
        find.byKey(const ValueKey('account-api-key')), 'sk-test');
    await tester.tap(find.widgetWithText(FilledButton, '创建'));
    await tester.pumpAndSettle();

    final body = jsonDecode(captured.single) as Map<String, dynamic>;
    final settings = body['quota_settings'] as Map<String, dynamic>;
    expect(settings, {
      'auto_interval_minutes': 5,
      'stop_interval_minutes': 8,
    });
  });

  testWidgets('quota section keeps the switch plus the two minute fields',
      (tester) async {
    await pumpForm(tester, editing: accountWithQuota);
    expect(find.text('额度查询'), findsOneWidget);
    expect(find.byKey(const ValueKey('quota-enabled')), findsOneWidget);
    expect(find.text('启用实时额度查询'), findsOneWidget);
    expect(find.byKey(const ValueKey('quota-interval')), findsOneWidget);
    expect(find.byKey(const ValueKey('quota-stop-interval')), findsOneWidget);
    for (final key in const [
      'quota-script-enabled',
      'quota-script-code',
      'quota-script-timeout',
      'quota-script-test',
      'script-var-add',
    ]) {
      expect(find.byKey(ValueKey(key)), findsNothing, reason: '$key 已移除');
    }
    expect(find.text('启用脚本'), findsNothing);
    expect(find.text('脚本代码'), findsNothing);
    expect(find.text('变量'), findsNothing);
  });

  testWidgets('quota switch sits above the interval fields', (tester) async {
    await pumpForm(tester, editing: accountWithQuota);
    final switchY = tester
        .getTopLeft(find.byKey(const ValueKey('quota-enabled')))
        .dy;
    for (final key in const ['quota-interval', 'quota-stop-interval']) {
      expect(switchY,
          lessThan(tester.getTopLeft(find.byKey(ValueKey(key))).dy),
          reason: '开关必须在编辑框上方');
    }
  });

  testWidgets('turning the quota switch off submits enabled false',
      (tester) async {
    final captured = <String>[];
    await pumpForm(tester,
        editing: accountWithQuota, client: recordingClient(captured));

    await tester.ensureVisible(find.byKey(const ValueKey('quota-enabled')));
    await tester.tap(find.byKey(const ValueKey('quota-enabled')));
    await tester.pumpAndSettle();
    expect(find.text('已关闭 · 该账号不查询额度'), findsOneWidget);

    await tester.ensureVisible(find.widgetWithText(FilledButton, '保存'));
    await tester.tap(find.widgetWithText(FilledButton, '保存'));
    await tester.pumpAndSettle();

    final body = jsonDecode(captured.single) as Map<String, dynamic>;
    expect(body['quota_settings'], {
      'enabled': false,
      'auto_interval_minutes': 5,
      'stop_interval_minutes': 8,
    });
  });

  testWidgets('editing a disabled account restores the switch off',
      (tester) async {
    await pumpForm(tester, editing: accountQuotaDisabled);

    final sw = tester.widget<Switch>(find.byKey(const ValueKey('quota-enabled')));
    expect(sw.value, isFalse);
    for (final key in const ['quota-interval', 'quota-stop-interval']) {
      final field =
          tester.widget<TextFormField>(find.byKey(ValueKey(key)));
      expect(field.enabled, isFalse, reason: '关掉查询后 $key 置灰');
    }
  });

  testWidgets('re-enabling a disabled account drops the enabled field',
      (tester) async {
    final captured = <String>[];
    await pumpForm(tester,
        editing: accountQuotaDisabled, client: recordingClient(captured));

    await tester.ensureVisible(find.byKey(const ValueKey('quota-enabled')));
    await tester.tap(find.byKey(const ValueKey('quota-enabled')));
    await tester.pumpAndSettle();

    await tester.ensureVisible(find.widgetWithText(FilledButton, '保存'));
    await tester.tap(find.widgetWithText(FilledButton, '保存'));
    await tester.pumpAndSettle();

    final body = jsonDecode(captured.single) as Map<String, dynamic>;
    expect(body['quota_settings'],
        {'auto_interval_minutes': 5, 'stop_interval_minutes': 0},
        reason: '开启态不落 enabled 字段,与「未表态即开启」同形');
  });

  testWidgets('create submits the visible default quota settings',
      (tester) async {
    final captured = <String>[];
    await pumpForm(tester, client: recordingClient(captured));

    await selectCascade(tester, vendor: 'DeepSeek', billing: '按量计费', region: '全球');
    await tester.enterText(find.byKey(const ValueKey('account-name')), 'ds-new');
    await tester.enterText(
        find.byKey(const ValueKey('account-api-key')), 'sk-test');
    await tester.tap(find.widgetWithText(FilledButton, '创建'));
    await tester.pumpAndSettle();

    final body = jsonDecode(captured.single) as Map<String, dynamic>;
    expect(body['quota_settings'], {
      'auto_interval_minutes': 0,
      'stop_interval_minutes': 5,
    }, reason: '全新表单停止间隔预填 5(与后端默认一致),创建时显式提交');
  });

  testWidgets('edit clearing the intervals sends an explicit empty object',
      (tester) async {
    final captured = <String>[];
    await pumpForm(tester,
        editing: accountWithQuota, client: recordingClient(captured));

    // 初始展开(已有配置):清空两个间隔=不要自定义节奏了
    await tester.enterText(find.byKey(const ValueKey('quota-interval')), '');
    await tester.enterText(
        find.byKey(const ValueKey('quota-stop-interval')), '');
    await tester.pumpAndSettle();

    await tester.ensureVisible(find.widgetWithText(FilledButton, '保存'));
    await tester.tap(find.widgetWithText(FilledButton, '保存'));
    await tester.pumpAndSettle();

    final body = jsonDecode(captured.single) as Map<String, dynamic>;
    expect(body['quota_settings'], isA<Map<String, dynamic>>().having(
        (m) => m.isEmpty, 'isEmpty', isTrue),
        reason: '原来有配置时清空要显式发空对象,服务端才清除而不是保留');
  });

  testWidgets('interval out of range fails validation', (tester) async {
    final captured = <String>[];
    await pumpForm(tester,
        editing: accountWithQuota, client: recordingClient(captured));

    await tester.enterText(
        find.byKey(const ValueKey('quota-interval')), '2000');
    await tester.pumpAndSettle();
    await tester.ensureVisible(find.widgetWithText(FilledButton, '保存'));
    await tester.tap(find.widgetWithText(FilledButton, '保存'));
    await tester.pumpAndSettle();

    expect(captured, isEmpty, reason: '校验失败不发请求');
    expect(find.text('需为 0-1440 的整数'), findsOneWidget);
  });

  // ── oauth_refresh(订阅登录态)表单分流 ──

  testWidgets('oauth provider shows login fields instead of api key',
      (tester) async {
    await pumpForm(tester);

    await selectCascade(tester, vendor: 'OpenAI', billing: '订阅', region: '全球');

    expect(find.byKey(const ValueKey('account-api-key')), findsNothing,
        reason: '订阅登录态不是静态密钥,密钥框不出现');
    expect(find.byKey(const ValueKey('account-refresh-token')), findsOneWidget);
    expect(find.byKey(const ValueKey('account-account-id')), findsOneWidget);
    expect(fieldText(tester, 'account-base-url'),
        'https://chatgpt.com/backend-api/codex',
        reason: '级联解析到 openai-codex 后带出其默认地址');
  });

  testWidgets('oauth create submits credential object not api_key',
      (tester) async {
    final captured = <String>[];
    await pumpForm(tester, client: recordingClient(captured));

    await selectCascade(tester, vendor: 'OpenAI', billing: '订阅', region: '全球');
    await tester.enterText(find.byKey(const ValueKey('account-name')), 'gpt-1');
    await tester.enterText(
        find.byKey(const ValueKey('account-refresh-token')), 'rt-live');
    await tester.enterText(
        find.byKey(const ValueKey('account-account-id')), 'acc-9');
    await tester.tap(find.widgetWithText(FilledButton, '创建'));
    await tester.pumpAndSettle();

    expect(captured, hasLength(1));
    final body = jsonDecode(captured.single) as Map<String, dynamic>;
    expect(body['provider_id'], 'openai.global.subscribe.codex');
    expect(body.containsKey('api_key'), isFalse,
        reason: 'oauth 形态不走 api_key 简写');
    final cred = body['credential'] as Map<String, dynamic>;
    expect(cred, {
      'kind': 'oauth_refresh',
      'refresh_token': 'rt-live',
      'account_id': 'acc-9',
    });
  });

  testWidgets('oauth create requires refresh token', (tester) async {
    final captured = <String>[];
    await pumpForm(tester, client: recordingClient(captured));

    await selectCascade(tester, vendor: 'OpenAI', billing: '订阅', region: '全球');
    await tester.enterText(find.byKey(const ValueKey('account-name')), 'gpt-1');
    await tester.enterText(
        find.byKey(const ValueKey('account-account-id')), 'acc-9');
    await tester.tap(find.widgetWithText(FilledButton, '创建'));
    await tester.pumpAndSettle();

    expect(captured, isEmpty, reason: '校验失败不发请求');
    expect(find.text('Refresh Token 不能为空'), findsOneWidget);
  });

  testWidgets('codex login card checklist shows import contents when empty',
      (tester) async {
    await pumpForm(tester);
    await selectCascade(tester, vendor: 'OpenAI', billing: '订阅', region: '全球');
    expect(find.text('Codex 登录态'), findsOneWidget);
    expect(find.text('未配置'), findsOneWidget);
    expect(find.text('待导入'), findsNWidgets(2));
    expect(find.text('从 codex CLI 导入'), findsOneWidget);
    expect(find.text('从 Codex App 导入'), findsOneWidget);
    expect(find.text('凭据详情(导入自动填充,一般无需修改)'), findsOneWidget);
  });

  testWidgets('codex login card checklist reflects stored credentials in edit',
      (tester) async {
    await pumpForm(tester, editing: accountOAuth);
    // 徽标 + Refresh Token + Account ID 三处「已配置」。
    expect(find.text('已配置'), findsNWidgets(3));
    expect(find.text('待导入'), findsNothing);
  });

  testWidgets('oauth edit prefills account id and keeps credential when blank',
      (tester) async {
    final captured = <String>[];
    await pumpForm(tester, editing: accountOAuth, client: recordingClient(captured));

    expect(fieldText(tester, 'account-account-id'), 'acc-123',
        reason: 'account_id 是标识不是秘密,预填回显');
    expect(find.textContaining('************'), findsOneWidget,
        reason: 'refresh_token 默认纯星号');
    expect(find.byKey(const ValueKey('oauth-reauth-banner')), findsNothing,
        reason: '未标记失效不出横幅');

    await tester.ensureVisible(find.widgetWithText(FilledButton, '保存'));
    await tester.tap(find.widgetWithText(FilledButton, '保存'));
    await tester.pumpAndSettle();

    final body = jsonDecode(captured.single) as Map<String, dynamic>;
    expect(body.containsKey('credential'), isFalse,
        reason: 'refresh_token 留空=保留原登录态,整体不动凭据');
  });

  testWidgets('oauth edit replaces credential when refresh token pasted',
      (tester) async {
    final captured = <String>[];
    await pumpForm(tester, editing: accountOAuth, client: recordingClient(captured));

    await tester.enterText(
        find.byKey(const ValueKey('account-refresh-token')), 'rt-new');
    await tester.ensureVisible(find.widgetWithText(FilledButton, '保存'));
    await tester.tap(find.widgetWithText(FilledButton, '保存'));
    await tester.pumpAndSettle();

    final body = jsonDecode(captured.single) as Map<String, dynamic>;
    final cred = body['credential'] as Map<String, dynamic>;
    expect(cred['kind'], 'oauth_refresh');
    expect(cred['refresh_token'], 'rt-new');
    expect(cred['account_id'], 'acc-123', reason: '预填的 account_id 随凭据一起提交');
  });

  testWidgets('oauth edit shows reauth banner when flagged', (tester) async {
    final flagged = Account.fromJson(const {
      'name': 'gpt-1',
      'provider_id': 'openai.global.subscribe.codex',
      'credential': {
        'kind': 'oauth_refresh',
        'refresh_token': 'rt-a***z',
        'account_id': 'acc-123',
      },
      'base_url': '',
      'enabled': true,
      'needs_reauth': true,
    });
    await pumpForm(tester, editing: flagged);

    expect(find.byKey(const ValueKey('oauth-reauth-banner')), findsOneWidget,
        reason: '服务端标记登录态终态失效时提示重新粘贴');
  });

  // ── 本机登录态获取:codex CLI 与 Codex App 两个独立入口 ──

  /// 建一个临时用户目录并覆写探测根,返回可写相对路径的辅助函数。
  String Function(String) useTempHome() {
    final dir = Directory.systemTemp.createTempSync('msu-auth-test');
    addTearDown(() {
      debugCodexHomeOverride = null;
      dir.deleteSync(recursive: true);
    });
    debugCodexHomeOverride = dir.path;
    return (rel) {
      final file = File('${dir.path}${Platform.pathSeparator}$rel');
      file.parent.createSync(recursive: true);
      return file.path;
    };
  }

  Future<void> tapAutofill(WidgetTester tester, String key) async {
    await tester.ensureVisible(find.byKey(ValueKey(key)));
    await tester.tap(find.byKey(ValueKey(key)));
    await tester.pump();
  }

  /// toast 2.4s 后自动滑出,快进补动画收尾,不给用例末留 pending timer。
  Future<void> drainToast(WidgetTester tester) async {
    await tester.pump(const Duration(seconds: 3));
    await tester.pumpAndSettle();
  }

  void writeCliAuth(String Function(String) at, String content) =>
      File(at('.codex${Platform.pathSeparator}auth.json'))
          .writeAsStringSync(content);

  void writeAppAuth(String Function(String) at, String content) =>
      File(at('.cc-switch${Platform.pathSeparator}codex_oauth_auth.json'))
          .writeAsStringSync(content);

  testWidgets('cli button fills both fields from codex CLI auth.json',
      (tester) async {
    final at = useTempHome();
    writeCliAuth(at,
        '{"tokens":{"refresh_token":"rt-from-file","account_id":"acc-from-file"}}');
    await pumpForm(tester);
    await selectCascade(tester, vendor: 'OpenAI', billing: '订阅', region: '全球');

    await tapAutofill(tester, 'oauth-autofill-cli');

    expect(fieldText(tester, 'account-refresh-token'), 'rt-from-file');
    expect(fieldText(tester, 'account-account-id'), 'acc-from-file');
    expect(find.text('已填入 codex CLI 的登录态'), findsOneWidget);
    await drainToast(tester);
  });

  testWidgets('cli button reports api-key login as no subscription auth',
      (tester) async {
    final at = useTempHome();
    // 你本机的真实形态:CLI 被 CC Switch 接管成 API Key 登录
    writeCliAuth(at, '{"OPENAI_API_KEY":"sk-x"}');
    await pumpForm(tester);
    await selectCascade(tester, vendor: 'OpenAI', billing: '订阅', region: '全球');

    await tapAutofill(tester, 'oauth-autofill-cli');

    expect(fieldText(tester, 'account-refresh-token'), isEmpty);
    expect(find.textContaining('codex CLI: '), findsOneWidget);
    expect(find.textContaining('API Key 登录'), findsOneWidget);
    await drainToast(tester);
  });

  testWidgets('app button fills from Codex App vault (CC Switch store)',
      (tester) async {
    final at = useTempHome();
    writeAppAuth(at,
        '{"default_account_id":"internal-1",'
        '"accounts":{"internal-1":{'
        '"account_id":"internal-1",'
        '"chatgpt_account_id":"chatgpt-acc-9",'
        '"refresh_token":"rt-cc"}}}');
    await pumpForm(tester);
    await selectCascade(tester, vendor: 'OpenAI', billing: '订阅', region: '全球');

    await tapAutofill(tester, 'oauth-autofill-app');

    expect(fieldText(tester, 'account-refresh-token'), 'rt-cc');
    expect(fieldText(tester, 'account-account-id'), 'chatgpt-acc-9',
        reason: '取 chatgpt_account_id 而非 CC Switch 内部 account_id');
    expect(find.text('已填入 Codex App 的登录态'), findsOneWidget);
    await drainToast(tester);
  });

  testWidgets('each button reports only its own source', (tester) async {
    useTempHome(); // 空目录:两个来源都不存在
    await pumpForm(tester);
    await selectCascade(tester, vendor: 'OpenAI', billing: '订阅', region: '全球');

    await tapAutofill(tester, 'oauth-autofill-app');
    expect(find.textContaining('未找到 Codex App 的登录态'), findsOneWidget);
    expect(find.textContaining('未找到 codex CLI 的登录态'), findsNothing,
        reason: 'Codex App 入口不探测、也不汇报 CLI 来源');
    await drainToast(tester);

    await tapAutofill(tester, 'oauth-autofill-cli');
    expect(find.textContaining('未找到 codex CLI 的登录态'), findsOneWidget);
    expect(fieldText(tester, 'account-refresh-token'), isEmpty);
    await drainToast(tester);
  });

  testWidgets('parse helpers handle their own shape and reject garbage',
      (tester) async {
    expect(
        parseCodexCliAuth(
            '{"tokens":{"refresh_token":"rt","account_id":"acc","access_token":"at"}}'),
        ('rt', 'acc'),
        reason: '多余字段忽略,只取登录续期所需两项');
    expect(() => parseCodexCliAuth('"just a string"'),
        throwsA(isA<FormatException>()));
    expect(() => parseCodexCliAuth('{"OPENAI_API_KEY":"sk-x"}'),
        throwsA(isA<FormatException>()),
        reason: '纯 API Key 登录没有订阅登录态');
    expect(
        parseCodexAppAuth('{"default_account_id":"i1","accounts":{"i1":'
            '{"refresh_token":"rt2","chatgpt_account_id":"cg-2"}}}'),
        ('rt2', 'cg-2'),
        reason: '按 default_account_id 取默认账号');
    expect(() => parseCodexAppAuth('{"accounts":{}}'),
        throwsA(isA<FormatException>()));
  });

  // ── kimi 网页会话 token:月度额度凭据的字段与自动获取 ──

  /// 造一个三段假 JWT(载荷自定义),满足 _jwtPattern 的形态。
  String fakeJwt(Map<String, dynamic> payload) {
    String seg(Map<String, dynamic> m) =>
        base64Url.encode(utf8.encode(jsonEncode(m))).replaceAll('=', '');
    return '${seg({'alg': 'RS256', 'typ': 'JWT'})}.${seg(payload)}.sig';
  }

  String fakeRefreshJwt(int exp) =>
      fakeJwt({'iss': 'user-center', 'typ': 'refresh', 'exp': exp});

  testWidgets('kimi form shows web token field, other providers do not',
      (tester) async {
    await pumpForm(tester, editing: account);
    expect(find.byKey(const ValueKey('account-web-refresh-token')),
        findsNothing,
        reason: 'deepseek 没有网页会话 token 字段');
  });

  testWidgets('kimi edit shows masked web token field with autofill button',
      (tester) async {
    await pumpForm(tester, editing: accountKimi);
    expect(
        find.byKey(const ValueKey('account-web-refresh-token')), findsOneWidget);
    expect(find.byKey(const ValueKey('kimi-autofill-desktop')), findsOneWidget);
    final field = tester.widget<TextFormField>(
        find.byKey(const ValueKey('account-web-refresh-token')));
    expect(field.controller!.text, isEmpty, reason: '编辑态 token 不回显明文');
    expect(find.textContaining('************'), findsWidgets,
        reason: '默认纯星号,眼睛才亮掩码');
    expect(find.textContaining('eyJh***xyz'), findsNothing,
        reason: '未点眼睛前网页 token 掩码字符一个都不露');
  });

  testWidgets('kimi web token card reflects stored token in edit',
      (tester) async {
    await pumpForm(tester, editing: accountKimi);
    expect(find.text('网页会话登录态'), findsOneWidget);
    // 徽标 + Token 清单项两处「已配置」。
    expect(find.text('已配置'), findsNWidgets(2));
    expect(find.text('从 kimi-desktop 导入'), findsOneWidget);
  });

  testWidgets('kimi create submits credential with web refresh token',
      (tester) async {
    final captured = <String>[];
    await pumpForm(tester, client: recordingClient(captured));

    await selectCascade(tester, vendor: 'Kimi', billing: '订阅', region: '中国');
    await tester.enterText(find.byKey(const ValueKey('account-name')), 'kimi-9');
    await tester.enterText(
        find.byKey(const ValueKey('account-api-key')), 'sk-kimi-9');
    await tester.enterText(
        find.byKey(const ValueKey('account-web-refresh-token')), 'web-rt-9');
    await tester.ensureVisible(find.widgetWithText(FilledButton, '创建'));
    await tester.tap(find.widgetWithText(FilledButton, '创建'));
    await tester.pumpAndSettle();

    expect(captured, hasLength(1));
    final body = jsonDecode(captured.single) as Map<String, dynamic>;
    final credential = body['credential'] as Map<String, dynamic>;
    expect(credential['kind'], 'api_key');
    expect(credential['api_key'], 'sk-kimi-9');
    expect(credential['web_refresh_token'], 'web-rt-9',
        reason: '网页会话 token 随 credential 上行,服务端月度链据此查询');
  });

  testWidgets('kimi desktop button fills web token from leveldb',
      (tester) async {
    final dir = Directory.systemTemp.createTempSync('msu-kimi-test');
    addTearDown(() {
      debugKimiDesktopDirOverride = null;
      dir.deleteSync(recursive: true);
    });
    debugKimiDesktopDirOverride = dir.path;
    final token = fakeRefreshJwt(4102444800);
    File('${dir.path}${Platform.pathSeparator}000123.log')
        .writeAsStringSync('noise"$token"noise');
    await pumpForm(tester, editing: accountKimi);

    // 网页 token 字段在额度查询分栏里,kimi 样本无节奏配置分栏初始收起,
    // 先展开再点按钮(折叠态被 ClipRect 裁到零高,点不中)。
    await tester.ensureVisible(find.text('额度查询'));
    await tester.tap(find.text('额度查询'));
    await tester.pumpAndSettle();
    await tester.ensureVisible(find.byKey(const ValueKey('kimi-autofill-desktop')));
    await tester.tap(find.byKey(const ValueKey('kimi-autofill-desktop')));
    await tester.pump();

    expect(fieldText(tester, 'account-web-refresh-token'), token);
    await tester.pump(const Duration(seconds: 3));
    await tester.pumpAndSettle();
  });

  testWidgets('kimi web token parser picks latest refresh JWT only',
      (tester) async {
    final older = fakeRefreshJwt(1000);
    final newer = fakeRefreshJwt(2000);
    final access = fakeJwt({'iss': 'user-center', 'typ': 'access', 'exp': 9999});
    final other = fakeJwt({'iss': 'someone-else', 'typ': 'refresh', 'exp': 9999});
    // leveldb 里 token 以带引号/二进制前缀的字符串存储,引号即分隔。
    final blobs = [utf8.encode('x"$older"y"$access"z"$other"w')];
    expect(parseKimiWebRefreshToken(blobs), older,
        reason: 'access/异 issuer 一律跳过');
    expect(parseKimiWebRefreshToken([...blobs, utf8.encode('q"$newer"q')]),
        newer,
        reason: '多个 refresh 取 exp 最大者');
    expect(() => parseKimiWebRefreshToken([utf8.encode('no tokens here')]),
        throwsA(isA<FormatException>()));
    expect(
        () => parseKimiWebRefreshToken(
            [utf8.encode('prefix"$access"suffix')]),
        throwsA(isA<FormatException>()),
        reason: '只有 access token 不算网页登录态');
  });
}

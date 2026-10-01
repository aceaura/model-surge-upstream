import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:msu_admin/api_client.dart';
import 'package:msu_admin/models.dart';
import 'package:msu_admin/pages/account_form.dart';
import 'package:msu_admin/theme.dart';

final providers = [
  ProviderSpec.fromJson(const {
    'id': 'deepseek',
    'display_name': 'DeepSeek',
    'website': 'https://platform.deepseek.com',
    'base_url': 'https://api.deepseek.com',
    'protocols': ['chat_completions'],
    'auth': 'bearer',
    'credential': 'api_key',
  }),
  ProviderSpec.fromJson(const {
    'id': 'openai',
    'display_name': 'OpenAI',
    'website': 'https://openai.com',
    'base_url': 'https://api.openai.com',
    'protocols': ['chat_completions', 'responses'],
    'auth': 'bearer',
    'credential': 'api_key',
  }),
];

final account = Account.fromJson(const {
  'name': 'ds-1',
  'provider_id': 'deepseek',
  'credential': {'kind': 'api_key', 'api_key': 'sk-d***efgh'},
  'base_url': 'https://ds.example.com',
  'headers': <String, dynamic>{'x-tenant': 'a'},
  'enabled': false,
});

/// 无 base_url 的账号:拷贝/编辑时请求地址应回落到提供商默认值。
final accountNoOverride = Account.fromJson(const {
  'name': 'ds-2',
  'provider_id': 'deepseek',
  'credential': {'kind': 'api_key', 'api_key': 'sk-x***y'},
  'base_url': '',
  'enabled': true,
});

/// 带额度脚本的账号:脚本区预填与清除语义的样本。
final accountWithScript = Account.fromJson(const {
  'name': 'ds-3',
  'provider_id': 'deepseek',
  'credential': {'kind': 'api_key', 'api_key': 'sk-s***t'},
  'base_url': '',
  'enabled': true,
  'quota_script': {
    'enabled': true,
    'code': '({request: {url: "{{baseUrl}}/x"}, extractor: function(r) { return {remaining: r.b, unit: "USD"}; }})',
    'timeout_seconds': 15,
    'auto_interval_minutes': 5,
  },
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
                'provider_id': 'deepseek',
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

/// 额度脚本区默认折叠,展开并滚入视野。
Future<void> expandScriptSection(WidgetTester tester) async {
  final header = find.text('额度脚本');
  await tester.ensureVisible(header);
  await tester.tap(header);
  await tester.pumpAndSettle();
  await tester.ensureVisible(find.byKey(const ValueKey('quota-script-code')));
  await tester.pumpAndSettle();
}

String fieldText(WidgetTester tester, String key) => tester
    .widget<TextFormField>(find.byKey(ValueKey(key)))
    .controller!
    .text;

void main() {
  testWidgets('copy prefills config but keeps create semantics', (tester) async {
    await pumpForm(tester, copyFrom: account);

    expect(find.text('拷贝账号 ds-1'), findsOneWidget);
    expect(find.text('DeepSeek (deepseek)'), findsOneWidget,
        reason: 'provider dropdown follows the source account');
    expect(fieldText(tester, 'account-name'), 'ds-1-copy');
    expect(fieldText(tester, 'account-base-url'), 'https://ds.example.com',
        reason: '已存覆盖值直接预填');
    expect(find.textContaining('原密钥不可见（sk-d***efgh），需重新填入'),
        findsOneWidget);
    expect(find.byType(SwitchListTile), findsNothing,
        reason: '启停由列表行开关控制,表单不再展示');
    expect(find.text('创建'), findsOneWidget,
        reason: 'copy is a create, not an edit');
  });

  testWidgets('copy falls back to provider default when no override',
      (tester) async {
    await pumpForm(tester, copyFrom: accountNoOverride);
    expect(fieldText(tester, 'account-base-url'), 'https://api.deepseek.com');
  });

  testWidgets('plain create stays blank but url prefills provider default',
      (tester) async {
    await pumpForm(tester);
    expect(find.text('新建账号'), findsOneWidget);
    expect(fieldText(tester, 'account-name'), isEmpty);
    expect(fieldText(tester, 'account-base-url'), 'https://api.deepseek.com',
        reason: '默认值是所选提供商的默认请求地址');
  });

  testWidgets('switching provider swaps an untouched default', (tester) async {
    await pumpForm(tester);

    await tester.tap(find.byKey(const ValueKey('account-provider')));
    await tester.pumpAndSettle();
    await tester.tap(find.text('OpenAI (openai)').last);
    await tester.pumpAndSettle();

    expect(fieldText(tester, 'account-base-url'), 'https://api.openai.com',
        reason: '用户没改过地址,换提供商时跟着换新默认值');
  });

  testWidgets('switching provider preserves a customized url', (tester) async {
    await pumpForm(tester);

    await tester.enterText(
        find.byKey(const ValueKey('account-base-url')),
        'https://my-gateway.example.com');
    await tester.pumpAndSettle();

    await tester.tap(find.byKey(const ValueKey('account-provider')));
    await tester.pumpAndSettle();
    await tester.tap(find.text('OpenAI (openai)').last);
    await tester.pumpAndSettle();

    expect(fieldText(tester, 'account-base-url'),
        'https://my-gateway.example.com');
  });

  testWidgets('submit sends empty override when url equals provider default',
      (tester) async {
    final captured = <String>[];
    await pumpForm(tester, client: recordingClient(captured));

    await tester.enterText(find.byKey(const ValueKey('account-name')), 'ds-new');
    await tester.enterText(
        find.byKey(const ValueKey('account-api-key')), 'sk-test');
    await tester.tap(find.widgetWithText(FilledButton, '创建'));
    await tester.pumpAndSettle();

    expect(captured, hasLength(1));
    final body = jsonDecode(captured.single) as Map<String, dynamic>;
    expect(body['base_url'], '',
        reason: '与提供商默认值相同→空覆盖,继续跟随提供商');
  });

  testWidgets('submit sends the customized url as override', (tester) async {
    final captured = <String>[];
    await pumpForm(tester, client: recordingClient(captured));

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

  testWidgets('copy prefills quota script and create submits it',
      (tester) async {
    final captured = <String>[];
    await pumpForm(tester,
        copyFrom: accountWithScript, client: recordingClient(captured));

    // 启用中的脚本让分栏初始展开,代码与数值直接回显
    expect(find.byKey(const ValueKey('quota-script-code')), findsOneWidget);
    expect(fieldText(tester, 'quota-script-code'),
        accountWithScript.quotaScript!.code);
    expect(fieldText(tester, 'quota-script-timeout'), '15');
    expect(fieldText(tester, 'quota-script-interval'), '5');
    expect(
        tester
            .widget<Switch>(find.byKey(const ValueKey('quota-script-enabled')))
            .value,
        isTrue);

    await tester.ensureVisible(find.byKey(const ValueKey('account-name')));
    await tester.enterText(find.byKey(const ValueKey('account-name')), 'ds-new');
    await tester.enterText(
        find.byKey(const ValueKey('account-api-key')), 'sk-test');
    await tester.tap(find.widgetWithText(FilledButton, '创建'));
    await tester.pumpAndSettle();

    final body = jsonDecode(captured.single) as Map<String, dynamic>;
    final script = body['quota_script'] as Map<String, dynamic>;
    expect(script['enabled'], isTrue);
    expect(script['code'], accountWithScript.quotaScript!.code);
    expect(script['timeout_seconds'], 15);
    expect(script['auto_interval_minutes'], 5);
  });

  testWidgets('create without touching script section omits the key',
      (tester) async {
    final captured = <String>[];
    await pumpForm(tester, client: recordingClient(captured));

    await tester.enterText(find.byKey(const ValueKey('account-name')), 'ds-new');
    await tester.enterText(
        find.byKey(const ValueKey('account-api-key')), 'sk-test');
    await tester.tap(find.widgetWithText(FilledButton, '创建'));
    await tester.pumpAndSettle();

    final body = jsonDecode(captured.single) as Map<String, dynamic>;
    expect(body.containsKey('quota_script'), isFalse,
        reason: '没配脚本就不发该字段,服务端保持未配置');
  });

  testWidgets('edit clearing the script sends an explicit empty object',
      (tester) async {
    final captured = <String>[];
    await pumpForm(tester,
        editing: accountWithScript, client: recordingClient(captured));

    // 初始展开(脚本启用中):关掉开关并清空代码=不要脚本了
    await tester.tap(find.byKey(const ValueKey('quota-script-enabled')));
    await tester.pumpAndSettle();
    await tester.enterText(find.byKey(const ValueKey('quota-script-code')), '');
    await tester.pumpAndSettle();

    await tester.ensureVisible(find.widgetWithText(FilledButton, '保存'));
    await tester.tap(find.widgetWithText(FilledButton, '保存'));
    await tester.pumpAndSettle();

    final body = jsonDecode(captured.single) as Map<String, dynamic>;
    expect(body['quota_script'], isA<Map<String, dynamic>>().having(
        (m) => m.isEmpty, 'isEmpty', isTrue),
        reason: '原来有配置时清空要显式发空对象,服务端才清除而不是保留');
  });

  testWidgets('enabled script with empty code fails validation',
      (tester) async {
    final captured = <String>[];
    await pumpForm(tester,
        editing: accountWithScript, client: recordingClient(captured));

    await tester.enterText(find.byKey(const ValueKey('quota-script-code')), '');
    await tester.pumpAndSettle();
    await tester.ensureVisible(find.widgetWithText(FilledButton, '保存'));
    await tester.tap(find.widgetWithText(FilledButton, '保存'));
    await tester.pumpAndSettle();

    expect(captured, isEmpty, reason: '校验失败不发请求');
    expect(find.text('启用脚本时代码不能为空'), findsOneWidget);
  });

  testWidgets('test button hits quota-test endpoint and shows result',
      (tester) async {
    String? hitPath;
    String? hitBody;
    final client = ApiClient(
      baseUrl: 'http://127.0.0.1:8080',
      adminKey: 'adm',
      httpClient: MockClient((req) async {
        hitPath = req.url.path;
        hitBody = req.body;
        return http.Response(
            jsonEncode({
              'ok': true,
              'report': {
                'account': 'ds-3',
                'queryable': true,
                'meters': [
                  {
                    'kind': 'balance',
                    'unit': 'currency',
                    'currency': 'USD',
                    'remaining': 9.5,
                  },
                ],
              },
            }),
            200,
            headers: {'content-type': 'application/json'});
      }),
    );
    await pumpForm(tester, editing: accountWithScript, client: client);

    await tester.ensureVisible(find.byKey(const ValueKey('quota-script-test')));
    await tester.tap(find.byKey(const ValueKey('quota-script-test')));
    await tester.pumpAndSettle();

    expect(hitPath, '/admin/accounts/ds-3/quota-test');
    expect(
        (jsonDecode(hitBody!) as Map<String, dynamic>)['code'],
        accountWithScript.quotaScript!.code);
    expect(find.textContaining('试跑成功'), findsOneWidget);
  });

  testWidgets('create mode disables the test button', (tester) async {
    await pumpForm(tester);
    await expandScriptSection(tester);

    final button = tester.widget<OutlinedButton>(
        find.widgetWithText(OutlinedButton, '试跑脚本'));
    expect(button.onPressed, isNull,
        reason: '试跑凭据取自已存账号,新建态不可试跑');
    expect(find.text('保存账号后才能试跑'), findsOneWidget);
  });
}

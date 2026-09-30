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
        copyFrom: copyFrom),
  ));
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
}

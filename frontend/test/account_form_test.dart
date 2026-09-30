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

ApiClient stubClient() => ApiClient(
      baseUrl: 'http://127.0.0.1:8080',
      adminKey: 'adm',
      httpClient: MockClient((_) async => http.Response('{}', 200)),
    );

Future<void> pumpForm(WidgetTester tester, {Account? copyFrom}) async {
  await tester.pumpWidget(MaterialApp(
    theme: buildAppTheme(),
    home: Scaffold(
      body: AccountForm(
          client: stubClient(), providers: providers, copyFrom: copyFrom),
    ),
  ));
  await tester.pumpAndSettle();
}

void main() {
  testWidgets('copy prefills config but keeps create semantics', (tester) async {
    await pumpForm(tester, copyFrom: account);

    expect(find.text('拷贝账号 ds-1'), findsOneWidget);
    expect(find.text('DeepSeek (deepseek)'), findsOneWidget,
        reason: 'provider dropdown follows the source account');
    final nameField = tester.widget<TextFormField>(find.ancestor(
        of: find.text('账号名'), matching: find.byType(TextFormField)));
    expect(nameField.controller!.text, 'ds-1-copy');
    final urlField = tester.widget<TextFormField>(find.ancestor(
        of: find.text('请求地址覆盖（可选）'),
        matching: find.byType(TextFormField)));
    expect(urlField.controller!.text, 'https://ds.example.com');
    expect(find.textContaining('原密钥不可见（sk-d***efgh），需重新填入'),
        findsOneWidget);
    expect(find.byType(SwitchListTile), findsNothing,
        reason: '启停由列表行开关控制,表单不再展示');
    expect(find.text('创建'), findsOneWidget,
        reason: 'copy is a create, not an edit');
  });

  testWidgets('plain create stays blank', (tester) async {
    await pumpForm(tester);
    expect(find.text('新建账号'), findsOneWidget);
    final nameField = tester.widget<TextFormField>(find.ancestor(
        of: find.text('账号名'), matching: find.byType(TextFormField)));
    expect(nameField.controller!.text, isEmpty);
  });
}

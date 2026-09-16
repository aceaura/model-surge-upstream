import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:msu_admin/api_client.dart';
import 'package:msu_admin/models.dart';
import 'package:msu_admin/pages/model_form.dart';

final providers = [
  ProviderSpec.fromJson(const {
    'id': 'kimi',
    'display_name': 'Moonshot Kimi',
    'website': 'https://platform.moonshot.cn',
    'base_url': 'https://api.moonshot.cn/coding',
    'protocols': ['anthropic', 'chat_completions'],
    'auth': 'anthropic_key',
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

final accounts = [
  Account.fromJson(const {
    'name': 'kimi-1',
    'provider_id': 'kimi',
    'credential': {'kind': 'api_key', 'api_key': 'sk-l***efgh'},
    'base_url': '',
    'headers': <String, dynamic>{},
    'enabled': true,
  }),
  Account.fromJson(const {
    'name': 'oa-1',
    'provider_id': 'openai',
    'credential': {'kind': 'api_key', 'api_key': 'sk-o***wxyz'},
    'base_url': '',
    'headers': <String, dynamic>{},
    'enabled': true,
  }),
];

ApiClient stubClient() => ApiClient(
      baseUrl: 'http://127.0.0.1:8080',
      adminKey: 'adm',
      httpClient: MockClient((_) async => http.Response('{}', 200)),
    );

Future<void> pumpForm(WidgetTester tester, {UpstreamModel? editing}) async {
  await tester.pumpWidget(MaterialApp(
    home: Scaffold(
      body: ModelForm(
        client: stubClient(),
        accounts: accounts,
        providers: providers,
        initialAccount: 'kimi-1',
        editing: editing,
      ),
    ),
  ));
  await tester.pumpAndSettle();
}

/// 找到指定 label 的下拉控件。
Finder dropdownFor(String label) => find.ancestor(
      of: find.text(label),
      matching: find.byType(DropdownButtonFormField<String>),
    );

void main() {
  testWidgets('protocol options come from the selected account provider',
      (tester) async {
    await pumpForm(tester);

    await tester.tap(dropdownFor('协议').first);
    await tester.pumpAndSettle();
    // kimi 支持 anthropic 与 chat_completions，不支持 responses。
    expect(find.text('anthropic'), findsWidgets);
    expect(find.text('responses'), findsNothing);

    await tester.tap(find.text('anthropic').last);
    await tester.pumpAndSettle();
  });

  testWidgets('switching account refreshes the protocol options',
      (tester) async {
    await pumpForm(tester);

    await tester.tap(dropdownFor('账号').first);
    await tester.pumpAndSettle();
    await tester.tap(find.text('oa-1 (openai)').last);
    await tester.pumpAndSettle();

    await tester.tap(dropdownFor('协议').first);
    await tester.pumpAndSettle();
    // openai 支持 responses 但不支持 anthropic。
    expect(find.text('responses'), findsWidgets);
    expect(find.text('anthropic'), findsNothing);
  });

  testWidgets('invalid params json disables the submit button', (tester) async {
    await pumpForm(tester);

    final submit = find.widgetWithText(FilledButton, '创建');
    expect(tester.widget<FilledButton>(submit).onPressed, isNotNull);

    await tester.enterText(find.widgetWithText(TextField, '默认参数'), '{bad');
    await tester.pump();

    expect(find.textContaining('JSON 格式错误'), findsOneWidget);
    expect(tester.widget<FilledButton>(submit).onPressed, isNull,
        reason: 'invalid json must block submission');

    await tester.enterText(
        find.widgetWithText(TextField, '默认参数'), '{"temperature":0.6}');
    await tester.pump();
    expect(tester.widget<FilledButton>(submit).onPressed, isNotNull);
  });

  testWidgets('editing prefills the existing values', (tester) async {
    final editing = UpstreamModel.fromJson(const {
      'id': 'kimi-1/k2',
      'account': 'kimi-1',
      'native_model': 'kimi-k2-turbo',
      'protocol': 'anthropic',
      'context_window': 262144,
      'defaults': {'temperature': 0.6},
      'overrides': {'max_tokens': 8192},
      'enabled': true,
    });
    await pumpForm(tester, editing: editing);

    expect(find.text('kimi-k2-turbo'), findsAtLeastNWidgets(1));
    expect(find.text('262144'), findsAtLeastNWidgets(1));
    expect(find.textContaining('"temperature": 0.6'), findsAtLeastNWidgets(1));
    expect(find.textContaining('"max_tokens": 8192'), findsAtLeastNWidgets(1));
    // 编辑态不允许改标识。
    final idField = tester.widget<TextField>(find.byWidgetPredicate(
      (w) => w is TextField && w.decoration?.labelText == '模型标识',
    ));
    expect(idField.enabled, isFalse);
  });

  testWidgets('context window rejects non-numeric input', (tester) async {
    await pumpForm(tester);
    await tester.enterText(find.widgetWithText(TextFormField, '上下文窗口'), 'abc');
    await tester.tap(find.widgetWithText(FilledButton, '创建'));
    await tester.pump();
    expect(find.text('请填写整数'), findsOneWidget);
  });
}

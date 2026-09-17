import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:msu_admin/api_client.dart';
import 'package:msu_admin/models.dart';
import 'package:msu_admin/pages/quota_dialog.dart';

final account = Account.fromJson(const {
  'name': 'ds-1',
  'provider_id': 'deepseek',
  'credential': {'kind': 'api_key', 'api_key': 'sk-d***efgh'},
  'base_url': '',
  'headers': <String, dynamic>{},
  'enabled': true,
});

Future<void> pumpDialog(
  WidgetTester tester,
  int status,
  Map<String, dynamic> body,
) async {
  final client = ApiClient(
    baseUrl: 'http://127.0.0.1:8080',
    adminKey: 'adm',
    httpClient: MockClient((_) async => http.Response(jsonEncode(body), status,
        headers: {'content-type': 'application/json'})),
  );
  await tester.pumpWidget(MaterialApp(
    home: Scaffold(body: QuotaDialog(client: client, account: account)),
  ));
  await tester.pumpAndSettle();
}

void main() {
  testWidgets('not-queryable shows a plain notice, not an error',
      (tester) async {
    await pumpDialog(
        tester, 200, {'account': 'ds-1', 'queryable': false, 'meters': []});

    await tester.tap(find.widgetWithText(FilledButton, '查询额度'));
    await tester.pumpAndSettle();

    expect(find.text('该提供商不支持额度查询。'), findsOneWidget);
    expect(find.textContaining('失败'), findsNothing);
  });

  testWidgets('successful query shows the balance and reset rule',
      (tester) async {
    await pumpDialog(tester, 200, {
      'account': 'ds-1',
      'queryable': true,
      'meters': [
        {
          'kind': 'balance',
          'unit': 'currency',
          'currency': 'CNY',
          'remaining': 12.34,
          'reset': 'prepaid',
        },
      ],
    });

    await tester.tap(find.widgetWithText(FilledButton, '查询额度'));
    await tester.pumpAndSettle();

    expect(find.text('12.34 CNY'), findsOneWidget);
    expect(find.text('prepaid'), findsOneWidget);
    expect(find.text('总量'), findsNothing,
        reason: 'absent figures should not get a row at all');
  });

  testWidgets('every meter gets its own section', (tester) async {
    await pumpDialog(tester, 200, {
      'account': 'ds-1',
      'queryable': true,
      'meters': [
        {
          'kind': 'usage',
          'unit': 'currency',
          'currency': 'USD',
          'used': 42,
          'reset': 'monthly',
        },
        {
          'kind': 'rate_limit',
          'unit': 'tokens',
          'label': 'tokens',
          'remaining': 9000,
          'total': 10000,
          'reset_at': '2026-10-01T00:00:00Z',
        },
      ],
    });

    await tester.tap(find.widgetWithText(FilledButton, '查询额度'));
    await tester.pumpAndSettle();

    expect(find.text('用量·金额'), findsOneWidget);
    expect(find.text('速率·tokens'), findsOneWidget);
    expect(find.text('已用'), findsOneWidget);
    expect(find.text('42.0 USD'), findsOneWidget);
    expect(find.text('9000.0 token'), findsOneWidget);
    expect(find.text('monthly'), findsOneWidget);
  });

  testWidgets('upstream failure shows the server message', (tester) async {
    await pumpDialog(tester, 502, {
      'error': {
        'code': 'quota_unavailable',
        'message': 'upstream quota query returned 401',
        'status': 502,
      }
    });

    await tester.tap(find.widgetWithText(FilledButton, '查询额度'));
    await tester.pumpAndSettle();

    expect(find.textContaining('upstream quota query returned 401'),
        findsOneWidget);
  });

  testWidgets('query starts idle and requires an explicit tap', (tester) async {
    await pumpDialog(
        tester, 200, {'account': 'ds-1', 'queryable': false, 'meters': []});
    expect(find.text('点击「查询额度」向上游发起一次查询。'), findsOneWidget);
  });
}

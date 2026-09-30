import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:msu_admin/api_client.dart';
import 'package:msu_admin/theme.dart';
import 'package:msu_admin/ui/quota_inline.dart';

ApiClient fakeClient(int status, Map<String, dynamic> body) => ApiClient(
      baseUrl: 'http://127.0.0.1:8080',
      adminKey: 'adm',
      httpClient: MockClient((_) async => http.Response(
          jsonEncode(body), status,
          headers: {'content-type': 'application/json'})),
    );

Future<void> pumpInline(
  WidgetTester tester, {
  required bool queryable,
  int status = 200,
  Map<String, dynamic> body = const {},
}) async {
  await tester.pumpWidget(MaterialApp(
    theme: buildAppTheme(),
    home: Scaffold(
      body: QuotaInline(
        client: fakeClient(status, body),
        accountName: 'ds-1',
        queryable: queryable,
      ),
    ),
  ));
  await tester.pumpAndSettle();
}

void main() {
  testWidgets('not queryable renders nothing', (tester) async {
    await pumpInline(tester, queryable: false);
    expect(find.byType(Text), findsNothing);
  });

  testWidgets('auto-queries on mount and shows compact balance',
      (tester) async {
    await pumpInline(tester, queryable: true, body: {
      'account': 'ds-1',
      'queryable': true,
      'meters': [
        {
          'kind': 'balance',
          'unit': 'currency',
          'currency': 'CNY',
          'remaining': 12.34,
        },
      ],
    });

    expect(find.text('余额 12.34 CNY'), findsOneWidget);
  });

  testWidgets('postpaid meter falls back to used figure', (tester) async {
    await pumpInline(tester, queryable: true, body: {
      'account': 'ds-1',
      'queryable': true,
      'meters': [
        {'kind': 'usage', 'unit': 'currency', 'currency': 'USD', 'used': 42},
      ],
    });

    expect(find.text('已用 42.0 USD'), findsOneWidget);
  });

  testWidgets('upstream failure offers a retry hint', (tester) async {
    await pumpInline(tester, queryable: true, status: 502, body: {
      'error': {
        'code': 'quota_unavailable',
        'message': 'upstream quota query returned 401',
        'status': 502,
      },
    });

    expect(find.textContaining('额度不可用'), findsOneWidget);
    expect(find.textContaining('401'), findsNothing,
        reason: '行内只放轻量提示,不搬上游报文');
  });
}

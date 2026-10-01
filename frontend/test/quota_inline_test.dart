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
  int autoIntervalMinutes = 0,
}) async {
  await tester.pumpWidget(MaterialApp(
    theme: buildAppTheme(),
    home: Scaffold(
      body: QuotaInline(
        client: fakeClient(status, body),
        accountName: 'ds-1',
        queryable: queryable,
        autoIntervalMinutes: autoIntervalMinutes,
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

  testWidgets('first line shows relative query time and refresh icon',
      (tester) async {
    await pumpInline(tester, queryable: true, body: {
      'account': 'ds-1',
      'queryable': true,
      'at': DateTime.now().toUtc().toIso8601String(),
      'meters': [
        {'kind': 'usage', 'unit': 'currency', 'currency': 'USD', 'used': 1},
      ],
    });

    expect(find.text('刚刚'), findsOneWidget);
    expect(find.byIcon(Icons.refresh), findsOneWidget);
    expect(find.byIcon(Icons.schedule), findsOneWidget);
  });

  testWidgets('percent meter shows bold colored figure and reset countdown',
      (tester) async {
    final resetAt = DateTime.now().add(const Duration(hours: 3, minutes: 8));
    await pumpInline(tester, queryable: true, body: {
      'account': 'ds-1',
      'queryable': true,
      'meters': [
        {
          'kind': 'usage',
          'unit': 'percent',
          'label': '5小时',
          'used': 64,
          'reset_at': resetAt.toUtc().toIso8601String(),
        },
      ],
    });

    // 构建到断言之间有时间流逝,倒计时期望值按同一公式现场算
    final d = resetAt.difference(DateTime.now());
    final cd = '${d.inHours}h${d.inMinutes % 60}m';
    final line = find.textContaining('5小时:');
    expect(line, findsOneWidget);
    final rich = tester.widget<Text>(line);
    final spans = (rich.textSpan! as TextSpan).children!;
    final bold =
        spans.whereType<TextSpan>().firstWhere((s) => s.text == '64%');
    expect(bold.style?.fontWeight, FontWeight.w600,
        reason: '百分比数字是 CC Switch 同款视觉锚点');
    final tokens = Theme.of(tester.element(line)).extension<AppTokens>()!;
    expect(bold.style?.color, tokens.success, reason: '64% < 70 水位为绿');
    expect(spans.whereType<TextSpan>().any((s) => s.text == cd), isTrue,
        reason: '倒计时紧随时钟图标,无 ⏱ 字符');
    expect(find.byIcon(Icons.schedule), findsNWidgets(2),
        reason: '时间行与倒计时各一个时钟图标');
  });

  testWidgets('percent color follows utilization thresholds', (tester) async {
    await pumpInline(tester, queryable: true, body: {
      'account': 'ds-1',
      'queryable': true,
      'meters': [
        {'kind': 'usage', 'unit': 'percent', 'label': '5小时', 'used': 75},
        {'kind': 'usage', 'unit': 'percent', 'label': '7天', 'used': 95},
      ],
    });

    final line = find.textContaining('5小时:');
    final rich = tester.widget<Text>(line);
    final spans = (rich.textSpan! as TextSpan).children!;
    final tokens = Theme.of(tester.element(line)).extension<AppTokens>()!;
    TextSpan spanOf(String text) =>
        spans.whereType<TextSpan>().firstWhere((s) => s.text == text);
    expect(spanOf('75%').style?.color, tokens.warn, reason: '70-89 水位为橙');
    expect(spanOf('95%').style?.color, tokens.danger, reason: '≥90 水位为红');
  });

  testWidgets('auto interval re-queries on schedule', (tester) async {
    var calls = 0;
    final client = ApiClient(
      baseUrl: 'http://127.0.0.1:8080',
      adminKey: 'adm',
      httpClient: MockClient((_) async {
        calls++;
        return http.Response(
            jsonEncode({
              'account': 'ds-1',
              'queryable': true,
              'meters': [
                {'kind': 'usage', 'unit': 'percent', 'used': 10},
              ],
            }),
            200,
            headers: {'content-type': 'application/json'});
      }),
    );
    await tester.pumpWidget(MaterialApp(
      theme: buildAppTheme(),
      home: Scaffold(
        body: QuotaInline(
          client: client,
          accountName: 'ds-1',
          queryable: true,
          autoIntervalMinutes: 1,
        ),
      ),
    ));
    await tester.pumpAndSettle();
    expect(calls, 1, reason: '挂载即查一次');

    await tester.pump(const Duration(minutes: 1, seconds: 1));
    await tester.pumpAndSettle();
    expect(calls, 2, reason: '到间隔自动重查');
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

import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:msu_admin/api_client.dart';
import 'package:msu_admin/models.dart';
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
  test('Credits model preserves only returned windows and RFC3339 reset', () {
    final report = QuotaReport.fromJson({
      'account': 'bl-1',
      'queryable': true,
      'meters': [
        {
          'kind': 'usage',
          'unit': 'credits',
          'label': '本月',
          'used': 0.9192,
          'total': 180000,
          'remaining': 179999.0808,
          'reset_at': '2026-11-01T00:00:00+08:00',
          'extra': 'Pro',
        },
      ],
    });
    expect(report.meters, hasLength(1));
    final m = report.meters.single;
    expect(m.used, 0.9192);
    expect(m.total, 180000);
    expect(m.remaining, 179999.0808);
    expect(m.resetAt, DateTime.utc(2026, 10, 31, 16));
    expect(m.extra, 'Pro');
  });

  testWidgets('Credits tiny usage shows nonzero percent and countdown only',
      (tester) async {
    final reset = DateTime.now().add(const Duration(days: 3, hours: 8));
    await pumpInline(tester, queryable: true, body: {
      'queryable': true,
      'meters': [
        {
          'kind': 'usage',
          'unit': 'credits',
          'label': '本月',
          'used': 0.9192,
          'total': 180000,
          'remaining': 179999.0808,
          'reset_at': reset.toUtc().toIso8601String(),
          'extra': 'Pro',
        },
      ],
    });
    final line = find.textContaining('本月:');
    final spans = (tester.widget<Text>(line).textSpan! as TextSpan).children!;
    final percent = spans.whereType<TextSpan>()
        .firstWhere((span) => span.text == '0.0005%');
    final tokens = Theme.of(tester.element(line)).extension<AppTokens>()!;
    expect(percent.style?.color, tokens.success);
    expect(percent.style?.fontWeight, FontWeight.w600);
    expect(spans.whereType<TextSpan>().map((s) => s.text ?? ''),
        everyElement(isNot(contains('Credits'))),
        reason: '行内只留百分比,不摊具体额度');
    final d = reset.difference(DateTime.now());
    expect(spans.whereType<TextSpan>().map((s) => s.text),
        contains('${d.inDays}d${d.inHours % 24}h'));
    expect(find.textContaining('5小时:'), findsNothing);
    expect(find.textContaining('7天:'), findsNothing,
        reason: '缺失窗口不补零');
  });

  testWidgets('Credits thresholds derive from used over total, not raw used',
      (tester) async {
    await pumpInline(tester, queryable: true, body: {
      'queryable': true,
      'meters': [
        {'unit': 'credits', 'label': '本月', 'used': 6400, 'total': 10000, 'remaining': 3600},
        {'unit': 'credits', 'label': '5小时', 'used': 15, 'total': 20, 'remaining': 5},
        {'unit': 'credits', 'label': '7天', 'used': 19, 'total': 20, 'remaining': 1},
      ],
    });
    final line = find.textContaining('本月:');
    final spans = (tester.widget<Text>(line).textSpan! as TextSpan).children!;
    final tokens = Theme.of(tester.element(line)).extension<AppTokens>()!;
    TextSpan spanOf(String text) =>
        spans.whereType<TextSpan>().firstWhere((s) => s.text == text);
    expect(spanOf('64%').style?.color, tokens.success);
    expect(spanOf('75%').style?.color, tokens.warn);
    expect(spanOf('95%').style?.color, tokens.danger);
    expect(spans.whereType<TextSpan>().map((s) => s.text ?? ''),
        everyElement(isNot(contains('剩余'))),
        reason: '行内不摊剩余额度');
  });

  for (final (used, expected) in [
    (0.0, '0%'),
    (0.00001, '0.000001%'),
    (0.0000001, '<0.000001%'),
  ]) {
    testWidgets('Credits adaptive percent renders $used as $expected',
        (tester) async {
      await pumpInline(tester, queryable: true, body: {
        'queryable': true,
        'meters': [{'unit': 'credits', 'label': '本月', 'used': used, 'total': 1000}],
      });
      final line = find.textContaining('本月:');
      final spans = (tester.widget<Text>(line).textSpan! as TextSpan).children!;
      expect(spans.whereType<TextSpan>().map((s) => s.text), contains(expected));
      expect(find.textContaining('剩余'), findsNothing,
          reason: '上游未提供 remaining 时不推算、不补零');
    });
  }

  testWidgets('Credits without positive total or used keep prior fallback',
      (tester) async {
    await pumpInline(tester, queryable: true, body: {
      'queryable': true,
      'meters': [
        {'unit': 'credits', 'label': '本月', 'used': 2, 'total': 0},
        {'unit': 'credits', 'label': '5小时', 'remaining': 3, 'total': 10},
        {'unit': 'credits', 'label': '7天', 'used': 1},
      ],
    });
    expect(find.textContaining('已用 2.0 点数'), findsOneWidget);
    expect(find.textContaining('余额 3.0 点数'), findsOneWidget);
    expect(find.textContaining('已用 1.0 点数'), findsOneWidget);
    expect(find.byType(Tooltip), findsNothing,
        reason: '成功态不挂 Tooltip');
  });

  testWidgets('unknown quota stays percent and currency stays unchanged',
      (tester) async {
    await pumpInline(tester, queryable: true, body: {
      'queryable': true,
      'meters': [
        {'unit': 'percent', 'label': '5小时', 'used': 12.34},
        {'unit': 'currency', 'currency': 'CNY', 'remaining': 180000.25},
      ],
    });
    final line = find.textContaining('5小时:');
    final spans = (tester.widget<Text>(line).textSpan! as TextSpan).children!;
    expect(spans.whereType<TextSpan>().map((s) => s.text),
        containsAll(['5小时:', '12.34%', '余额 180000.25 CNY']));
    expect(find.byType(Tooltip), findsNothing,
        reason: '成功态不挂 Tooltip');
  });

  testWidgets('unknown total preserves tiny nonzero percent', (tester) async {
    await pumpInline(tester, queryable: true, body: {
      'queryable': true,
      'meters': [
        {'unit': 'percent', 'label': '本月', 'used': 0.0005106666666666667},
      ],
    });
    expect(find.textContaining('本月:0.0005%'), findsOneWidget);
    expect(find.textContaining('Credits'), findsNothing);
  });

  for (final reason in [
    '缺少额度查询 Token,请先运行 bl auth login --console',
    '额度查询 Token 已过期,请重新登录',
  ]) {
    testWidgets('quota failure tooltip explains actionable cause: $reason',
        (tester) async {
      await pumpInline(tester, queryable: true, status: 502, body: {
        'error': {'code': 'quota_unavailable', 'message': reason},
      });
      expect(find.text('额度不可用 · 点击重试'), findsOneWidget);
      expect(find.text(reason), findsNothing, reason: '原因不挤入列表行');
      expect(tester.widget<Tooltip>(find.byType(Tooltip)).message,
          '$reason\n点击重新查询额度');
    });
  }

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

    // 刷新钮贴住时间文案,中间不留空隙。
    final timeRight = tester.getRect(find.text('刚刚')).right;
    final refreshLeft = tester.getRect(find.byIcon(Icons.refresh)).left;
    expect(refreshLeft - timeRight, lessThanOrEqualTo(1));

    // 整块右对齐(CC Switch items-end):时间行右沿与计量行右沿齐平。
    final refreshRowRight = tester.getRect(find.byType(InkWell)).right;
    final metersRight = tester.getRect(find.textContaining('已用')).right;
    expect(refreshRowRight, moreOrLessEquals(metersRight, epsilon: 0.5));
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

  testWidgets('third meter renders on the same summary line', (tester) async {
    await pumpInline(tester, queryable: true, body: {
      'account': 'ds-1',
      'queryable': true,
      'meters': [
        {'kind': 'usage', 'unit': 'percent', 'label': '5小时', 'used': 20},
        {'kind': 'usage', 'unit': 'percent', 'label': '7天', 'used': 69},
        {'kind': 'usage', 'unit': 'percent', 'label': '本月', 'used': 3},
      ],
    });

    final line = find.textContaining('5小时:');
    expect(line, findsOneWidget);
    final spans =
        (tester.widget<Text>(line).textSpan! as TextSpan).children!;
    final texts = spans.whereType<TextSpan>().map((s) => s.text).toList();
    expect(texts, containsAll(['5小时:', '20%', '7天:', '69%', '本月:', '3%']),
        reason: '月度计量与速率窗口同行展示,不再被截断到两条');
  });

  testWidgets('auto interval re-queries on schedule', (tester) async {
    final urls = <String>[];
    final client = ApiClient(
      baseUrl: 'http://127.0.0.1:8080',
      adminKey: 'adm',
      httpClient: MockClient((req) async {
        urls.add(req.url.toString());
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
    expect(urls, hasLength(1), reason: '挂载即查一次');
    expect(urls.single, isNot(contains('auto=1')),
        reason: '首次加载是用户看页触发,非定时轮询');

    await tester.pump(const Duration(minutes: 1, seconds: 1));
    await tester.pumpAndSettle();
    expect(urls, hasLength(2), reason: '到间隔自动重查');
    expect(urls.last, contains('auto=1'),
        reason: '定时轮询带 auto=1,服务端据此对空闲账号短路不打上游');
  });

  testWidgets('refresh icon forces a cache-bypassing re-query', (tester) async {
    final urls = <String>[];
    final client = ApiClient(
      baseUrl: 'http://127.0.0.1:8080',
      adminKey: 'adm',
      httpClient: MockClient((req) async {
        urls.add(req.url.toString());
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
        ),
      ),
    ));
    await tester.pumpAndSettle();
    expect(urls, hasLength(1), reason: '挂载即查一次');
    expect(urls.single, isNot(contains('refresh=1')),
        reason: '首次加载走服务端缓存,不必强制打上游');

    await tester.tap(find.byIcon(Icons.refresh));
    await tester.pumpAndSettle();
    expect(urls, hasLength(2), reason: '点刷新钮重新请求额度');
    expect(urls.last, contains('refresh=1'),
        reason: '手动刷新必须绕过服务端缓存,否则 TTL 内只刷新时间戳');
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

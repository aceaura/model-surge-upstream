import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:msu_admin/api_client.dart';
import 'package:msu_admin/pages/accounts_page.dart';
import 'package:msu_admin/theme.dart';

/// 列表数据固定一条账号;account-test 的响应由 testResult 参数注入。
ApiClient fakeClient(Map<String, dynamic> testResult) => ApiClient(
      baseUrl: 'http://127.0.0.1:8080',
      adminKey: 'adm',
      httpClient: MockClient((request) async {
        final path = request.url.path;
        final Map<String, dynamic> payload;
        if (path.startsWith('/admin/account-test/')) {
          payload = testResult;
        } else if (path == '/admin/accounts') {
          payload = {
            'accounts': [
              {
                'name': 'ds-1',
                'provider_id': 'deepseek',
                'credential': {'api_key': 'sk-***'},
                'base_url': 'https://api.deepseek.com',
                'headers': <String, dynamic>{},
                'enabled': true,
              }
            ]
          };
        } else if (path == '/admin/providers') {
          // 空列表:行副标题回退到账号 base_url,额度摘要不出现。
          payload = {'providers': <dynamic>[]};
        } else {
          payload = {};
        }
        return http.Response(jsonEncode(payload), 200,
            headers: {'content-type': 'application/json'});
      }),
    );

Future<void> pumpPage(WidgetTester tester, Map<String, dynamic> testResult) async {
  tester.view.physicalSize = const Size(1400, 1000);
  tester.view.devicePixelRatio = 1.0;
  addTearDown(tester.view.reset);
  await tester.pumpWidget(MaterialApp(
    theme: buildAppTheme(),
    home: Scaffold(
      body: AccountsPage(
        client: fakeClient(testResult),
        onOpenSettings: () {},
        onOpenModels: (_) {},
      ),
    ),
  ));
  await tester.pumpAndSettle();
}

void main() {
  testWidgets('账号行检测按钮排第三位,成功结果走顶部提示框报时延', (tester) async {
    await pumpPage(tester, {'ok': true, 'status_code': 200, 'latency_ms': 123});

    final btn = find.byTooltip('检测连通性');
    expect(btn, findsOneWidget);
    expect(find.byTooltip('编辑'), findsOneWidget);
    expect(find.byTooltip('拷贝'), findsOneWidget);
    expect(find.byTooltip('模型'), findsOneWidget);
    expect(find.byTooltip('删除'), findsOneWidget);
    // 排位锁 CC Switch 式:编辑 < 拷贝 < 检测 < 模型 < 删除。
    double xOf(String tooltip) => tester.getCenter(find.byTooltip(tooltip)).dx;
    expect(xOf('编辑') < xOf('拷贝'), isTrue);
    expect(xOf('拷贝') < xOf('检测连通性'), isTrue);
    expect(xOf('检测连通性') < xOf('模型'), isTrue);
    expect(xOf('模型') < xOf('删除'), isTrue);

    await tester.tap(btn);
    await tester.pumpAndSettle();
    expect(find.textContaining('账号 ds-1 可达 · HTTP 200 · 123 ms'),
        findsOneWidget);

    // 冲刷提示框的 2.4s 驻留定时器与滑出动画,避免遗留 Timer。
    await tester.pump(const Duration(seconds: 3));
    await tester.pumpAndSettle();
  });

  testWidgets('检测失败提示框带原因', (tester) async {
    await pumpPage(tester, {
      'ok': false,
      'status_code': 0,
      'latency_ms': 87,
      'error': '上游不可达：connection refused',
    });

    await tester.tap(find.byTooltip('检测连通性'));
    await tester.pumpAndSettle();
    expect(find.textContaining('检测失败'), findsOneWidget);
    expect(find.textContaining('上游不可达'), findsOneWidget);

    await tester.pump(const Duration(seconds: 3));
    await tester.pumpAndSettle();
  });
}

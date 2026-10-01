import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:msu_admin/api_client.dart';
import 'package:msu_admin/models.dart';
import 'package:msu_admin/pages/all_models_page.dart';
import 'package:msu_admin/theme.dart';

/// 列表数据固定一条模型;model-test 的响应由 testResult 参数注入。
ApiClient fakeClient(Map<String, dynamic> testResult) => ApiClient(
      baseUrl: 'http://127.0.0.1:8080',
      adminKey: 'adm',
      httpClient: MockClient((request) async {
        final path = request.url.path;
        final Map<String, dynamic> payload;
        if (path.startsWith('/admin/model-test/')) {
          payload = testResult;
        } else if (path == '/admin/models') {
          payload = {
            'models': [
              {
                'id': 'ds-1/v4',
                'account': 'ds-1',
                'native_model': 'deepseek-v4.1-flash',
                'protocol': 'chat_completions',
                'context_window': 1000000,
                'defaults': <String, dynamic>{},
                'overrides': <String, dynamic>{},
                'enabled': true,
              }
            ]
          };
        } else if (path == '/admin/accounts') {
          payload = {'accounts': <dynamic>[]};
        } else if (path == '/admin/providers') {
          payload = {'providers': <dynamic>[]};
        } else {
          payload = {};
        }
        return http.Response(jsonEncode(payload), 200,
            headers: {'content-type': 'application/json'});
      }),
    );

Future<void> pumpPage(WidgetTester tester, Map<String, dynamic> testResult,
    {void Function(UpstreamModel)? onOpenUsage}) async {
  tester.view.physicalSize = const Size(1400, 1000);
  tester.view.devicePixelRatio = 1.0;
  addTearDown(tester.view.reset);
  await tester.pumpWidget(MaterialApp(
    theme: buildAppTheme(),
    home: Scaffold(
      body: AllModelsPage(
        client: fakeClient(testResult),
        onOpenSettings: () {},
        onOpenUsage: onOpenUsage,
      ),
    ),
  ));
  await tester.pumpAndSettle();
}

void main() {
  testWidgets('模型行有检测连通性按钮,成功结果走 snackbar 报时延', (tester) async {
    await pumpPage(tester, {'ok': true, 'status_code': 200, 'latency_ms': 123});

    final btn = find.byTooltip('检测连通性');
    expect(btn, findsOneWidget);
    // 编辑/拷贝/删除三个原按钮仍在。
    expect(find.byTooltip('编辑'), findsOneWidget);
    expect(find.byTooltip('拷贝'), findsOneWidget);
    expect(find.byTooltip('删除'), findsOneWidget);
    // 排位锁第三(CC Switch 式):编辑 < 拷贝 < 检测 < 删除。
    double xOf(String tooltip) => tester.getCenter(find.byTooltip(tooltip)).dx;
    expect(xOf('编辑') < xOf('拷贝'), isTrue);
    expect(xOf('拷贝') < xOf('检测连通性'), isTrue);
    expect(xOf('检测连通性') < xOf('删除'), isTrue);

    await tester.tap(btn);
    await tester.pumpAndSettle();
    expect(find.textContaining('ds-1/v4 连通正常 · HTTP 200 · 123 ms'),
        findsOneWidget);
  });

  testWidgets('第四位统计按钮:排位锁在检测与删除之间,点击上抛模型', (tester) async {
    UpstreamModel? opened;
    await pumpPage(tester, {'ok': true, 'status_code': 200, 'latency_ms': 1},
        onOpenUsage: (m) => opened = m);

    expect(find.byTooltip('统计'), findsOneWidget);
    // 排位(CC Switch 式):编辑 < 拷贝 < 检测 < 统计 < 删除。
    double xOf(String tooltip) => tester.getCenter(find.byTooltip(tooltip)).dx;
    expect(xOf('编辑') < xOf('拷贝'), isTrue);
    expect(xOf('拷贝') < xOf('检测连通性'), isTrue);
    expect(xOf('检测连通性') < xOf('统计'), isTrue);
    expect(xOf('统计') < xOf('删除'), isTrue);

    await tester.tap(find.byTooltip('统计'));
    expect(opened?.id, 'ds-1/v4');
  });

  testWidgets('检测失败 snackbar 带状态码与上游说明', (tester) async {
    await pumpPage(tester, {
      'ok': false,
      'status_code': 401,
      'latency_ms': 87,
      'error': '上游返回 HTTP 401：bad key',
    });

    await tester.tap(find.byTooltip('检测连通性'));
    await tester.pumpAndSettle();
    expect(find.textContaining('检测失败'), findsOneWidget);
    expect(find.textContaining('HTTP 401'), findsOneWidget);
  });

  testWidgets('重新激活(active false→true)重新拉取列表,切页回来能看到别处的删改',
      (tester) async {
    // 页在 IndexedStack 里常驻:删账号级联删模型后切回模型页,
    // 必须重拉列表,否则已删模型还挂在列表里。
    var modelsCalls = 0;
    final client = ApiClient(
      baseUrl: 'http://127.0.0.1:8080',
      adminKey: 'adm',
      httpClient: MockClient((request) async {
        final path = request.url.path;
        if (path == '/admin/models' && request.method == 'GET') {
          modelsCalls++;
        }
        final Map<String, dynamic> payload;
        if (path == '/admin/models') {
          payload = {'models': <dynamic>[]};
        } else if (path == '/admin/accounts') {
          payload = {'accounts': <dynamic>[]};
        } else if (path == '/admin/providers') {
          payload = {'providers': <dynamic>[]};
        } else {
          payload = {};
        }
        return http.Response(jsonEncode(payload), 200,
            headers: {'content-type': 'application/json'});
      }),
    );

    tester.view.physicalSize = const Size(1400, 1000);
    tester.view.devicePixelRatio = 1.0;
    addTearDown(tester.view.reset);
    Widget host(bool active) => MaterialApp(
          theme: buildAppTheme(),
          home: Scaffold(
            body: AllModelsPage(
                client: client, onOpenSettings: () {}, active: active),
          ),
        );

    await tester.pumpWidget(host(false));
    await tester.pumpAndSettle();
    expect(modelsCalls, 1); // 首载一次

    await tester.pumpWidget(host(false));
    await tester.pumpAndSettle();
    expect(modelsCalls, 1); // 保持后台不重拉

    await tester.pumpWidget(host(true));
    await tester.pumpAndSettle();
    expect(modelsCalls, 2); // 激活沿重拉一次
  });
}

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
  testWidgets('拖拽柄行首常驻,拖拽换序乐观换行并落库全量新顺序', (tester) async {
    final captured = <String>[];
    // 有状态桩:reorder 落库后的重拉按新顺序供数,贴近真实服务端。
    var serverOrder = ['a-1/m1', 'b-1/m2', 'c-1/m3'];
    final client = ApiClient(
      baseUrl: 'http://127.0.0.1:8080',
      adminKey: 'adm',
      httpClient: MockClient((request) async {
        final path = request.url.path;
        final Map<String, dynamic> payload;
        if (path == '/admin/models/reorder') {
          captured.add(request.body);
          serverOrder =
              (jsonDecode(request.body)['ids'] as List).cast<String>();
          payload = {'ok': true};
        } else if (path == '/admin/models') {
          payload = {
            'models': [
              for (final id in serverOrder)
                {
                  'id': id,
                  'account': id.split('/').first,
                  'native_model': 'native-$id',
                  'protocol': 'chat_completions',
                  'context_window': 0,
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

    tester.view.physicalSize = const Size(1400, 1000);
    tester.view.devicePixelRatio = 1.0;
    addTearDown(tester.view.reset);
    await tester.pumpWidget(MaterialApp(
      theme: buildAppTheme(),
      home: Scaffold(
        body: AllModelsPage(client: client, onOpenSettings: () {}),
      ),
    ));
    await tester.pumpAndSettle();

    double yOf(String id) => tester.getCenter(find.text(id)).dy;
    expect(yOf('a-1/m1') < yOf('b-1/m2'), isTrue);
    expect(yOf('b-1/m2') < yOf('c-1/m3'), isTrue);
    expect(find.byType(ReorderableDragStartListener), findsNWidgets(3));

    // 命中区不再是 18px 小图标:28 宽、撑满行高
    final grip =
        tester.getSize(find.byType(ReorderableDragStartListener).first);
    expect(grip.width, 28);
    expect(grip.height, greaterThan(40));

    await tester.drag(
        find.byType(ReorderableDragStartListener).first, const Offset(0, 260));
    await tester.pumpAndSettle();

    expect(captured, hasLength(1));
    expect(jsonDecode(captured.single), {
      'ids': ['b-1/m2', 'c-1/m3', 'a-1/m1']
    });
    expect(yOf('b-1/m2') < yOf('c-1/m3'), isTrue);
    expect(yOf('c-1/m3') < yOf('a-1/m1'), isTrue);

    // 外部(API/他端)改序后,激活重拉必须服从服务器序:落库成功的乐观序
    // 已放手,不能反过来盖住服务器。
    serverOrder = ['c-1/m3', 'a-1/m1', 'b-1/m2'];
    await tester.pumpWidget(MaterialApp(
      theme: buildAppTheme(),
      home: Scaffold(
        body: AllModelsPage(
          client: client,
          onOpenSettings: () {},
          active: true,
        ),
      ),
    ));
    await tester.pumpAndSettle();
    expect(yOf('c-1/m3') < yOf('a-1/m1'), isTrue);
    expect(yOf('a-1/m1') < yOf('b-1/m2'), isTrue);
  });

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

    // 冲刷提示框的 2.4s 驻留定时器与滑出动画,避免遗留 Timer。
    await tester.pump(const Duration(seconds: 3));
    await tester.pumpAndSettle();
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

  testWidgets('检测失败提示框带状态码与上游说明', (tester) async {
    await pumpPage(tester, {
      'ok': false,
      'status_code': 401,
      'latency_ms': 87,
      'error': '上游返回 HTTP 401：bad key',
    });

    await tester.tap(find.byTooltip('检测连通性'));
    await tester.pumpAndSettle();
    // 多行提示按 \n 拆成两排:首排带状态码,次排带上游说明。
    expect(find.textContaining('检测失败：HTTP 401'), findsOneWidget);
    expect(find.textContaining('上游返回 HTTP 401：bad key'), findsOneWidget);

    await tester.pump(const Duration(seconds: 3));
    await tester.pumpAndSettle();
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

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
  testWidgets('拖拽柄行首常驻,拖拽换序乐观换行并落库全量新顺序', (tester) async {
    final captured = <String>[];
    // 有状态桩:reorder 落库后的重拉按新顺序供数,贴近真实服务端。
    var serverOrder = ['a-1', 'b-1', 'c-1'];
    final client = ApiClient(
      baseUrl: 'http://127.0.0.1:8080',
      adminKey: 'adm',
      httpClient: MockClient((request) async {
        final path = request.url.path;
        final Map<String, dynamic> payload;
        if (path == '/admin/accounts/reorder') {
          captured.add(request.body);
          serverOrder =
              (jsonDecode(request.body)['names'] as List).cast<String>();
          payload = {'ok': true};
        } else if (path == '/admin/accounts') {
          payload = {
            'accounts': [
              for (final n in serverOrder)
                {
                  'name': n,
                  'provider_id': 'deepseek',
                  'credential': {'api_key': 'sk-***'},
                  'base_url': 'https://api.deepseek.com',
                  'headers': <String, dynamic>{},
                  'enabled': true,
                }
            ]
          };
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
        body: AccountsPage(
          client: client,
          onOpenSettings: () {},
          onOpenModels: (_) {},
        ),
      ),
    ));
    await tester.pumpAndSettle();

    double yOf(String name) => tester.getCenter(find.text(name)).dy;
    expect(yOf('a-1') < yOf('b-1'), isTrue);
    expect(yOf('b-1') < yOf('c-1'), isTrue);
    // 行首拖拽柄:每行一个
    expect(find.byType(ReorderableDragStartListener), findsNWidgets(3));

    // 拖 a-1 的柄越过两行落到底部
    await tester.drag(
        find.byType(ReorderableDragStartListener).first, const Offset(0, 260));
    await tester.pumpAndSettle();

    expect(captured, hasLength(1));
    expect(jsonDecode(captured.single), {
      'names': ['b-1', 'c-1', 'a-1']
    });
    expect(yOf('b-1') < yOf('c-1'), isTrue);
    expect(yOf('c-1') < yOf('a-1'), isTrue);

    // 外部(API/他端)改序后,激活重拉必须服从服务器序:落库成功的乐观序
    // 已放手,不能反过来盖住服务器。
    serverOrder = ['c-1', 'a-1', 'b-1'];
    await tester.pumpWidget(MaterialApp(
      theme: buildAppTheme(),
      home: Scaffold(
        body: AccountsPage(
          client: client,
          onOpenSettings: () {},
          onOpenModels: (_) {},
          active: true,
        ),
      ),
    ));
    await tester.pumpAndSettle();
    expect(yOf('c-1') < yOf('a-1'), isTrue);
    expect(yOf('a-1') < yOf('b-1'), isTrue);
  });

  testWidgets('搜索过滤态禁拖:行首不出现拖拽柄', (tester) async {
    await pumpPage(tester, {});
    await tester.enterText(find.byType(TextField).first, 'ds');
    await tester.pumpAndSettle();
    expect(find.text('ds-1'), findsOneWidget);
    expect(find.byType(ReorderableDragStartListener), findsNothing);
  });

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

  testWidgets('重新激活(active false→true)重新拉取列表,切页回来能看到后配的额度脚本',
      (tester) async {
    // 页在 IndexedStack 里常驻:外面给账号配了额度脚本后切回账号页,
    // 必须重拉列表,否则行内额度永不出现。
    var accountsCalls = 0;
    final client = ApiClient(
      baseUrl: 'http://127.0.0.1:8080',
      adminKey: 'adm',
      httpClient: MockClient((request) async {
        final path = request.url.path;
        if (path == '/admin/accounts' && request.method == 'GET') {
          accountsCalls++;
        }
        final Map<String, dynamic> payload;
        if (path == '/admin/accounts') {
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
            body: AccountsPage(
              client: client,
              onOpenSettings: () {},
              onOpenModels: (_) {},
              active: active,
            ),
          ),
        );

    await tester.pumpWidget(host(false));
    await tester.pumpAndSettle();
    expect(accountsCalls, 1); // 首载一次

    await tester.pumpWidget(host(false));
    await tester.pumpAndSettle();
    expect(accountsCalls, 1); // 保持后台不重拉

    await tester.pumpWidget(host(true));
    await tester.pumpAndSettle();
    expect(accountsCalls, 2); // 激活沿重拉一次
  });
}

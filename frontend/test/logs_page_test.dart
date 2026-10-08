import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:msu_admin/api_client.dart';
import 'package:msu_admin/pages/logs_page.dart';
import 'package:msu_admin/theme.dart';

/// 日志页模型/账号筛选的组件测试:
/// 筛选按 msg 里的 model=/account= 键值匹配,无该键的条目在筛选生效时隐藏;
/// 下拉选项 = 配置列表 ∪ 日志里实际出现过的值(已删模型的历史条目也可筛)。
ApiClient _stubClient({List<String>? extraModels}) {
  final mock = MockClient((req) async {
    final path = req.url.path;
    Object body = const {};
    if (path.endsWith('/admin/logs')) {
      body = {
        'entries': [
          {
            'seq': 1,
            'at': '2026-10-08T04:00:01Z',
            'level': 'info',
            'source': 'proxy',
            'msg':
                '→ POST https://runtime.us-east-1.kiro.dev/v1/messages model=kiro-1/claude-sonnet-5 account=kiro-1 native=claude-sonnet-5 body={}'
          },
          {
            'seq': 2,
            'at': '2026-10-08T04:00:02Z',
            'level': 'info',
            'source': 'resolve',
            'msg':
                'resolve model=kimi-1/k3-256k → account=kimi-1 protocol=chat_completions native=k3-256k'
          },
          {
            'seq': 3,
            'at': '2026-10-08T04:00:03Z',
            'level': 'info',
            'source': 'http',
            'msg': 'GET /admin/logs → 200 (1ms)'
          },
          {
            'seq': 4,
            'at': '2026-10-08T04:00:04Z',
            'level': 'info',
            'source': 'chat',
            'msg': 'chat session=abc model=old-1/gone account=old-1 reply=12 chars'
          },
        ],
        'next': 5,
      };
    } else if (path.endsWith('/admin/models')) {
      body = {
        'models': [
          {'id': 'kiro-1/claude-sonnet-5'},
          {'id': 'kimi-1/k3-256k'},
          for (final id in extraModels ?? const <String>[]) {'id': id},
        ]
      };
    } else if (path.endsWith('/admin/accounts')) {
      body = {
        'accounts': [
          {'name': 'kiro-1'},
          {'name': 'kimi-1'},
        ]
      };
    }
    return http.Response(jsonEncode(body), 200,
        headers: {'content-type': 'application/json'});
  });
  return ApiClient(baseUrl: 'http://stub', adminKey: 'k', httpClient: mock);
}

Future<void> _pumpLogs(WidgetTester tester,
    {bool active = false, List<String>? extraModels}) async {
  // 页头四个下拉+按钮排一行,默认 800 宽测窗放不下会报溢出。
  tester.view.physicalSize = const Size(1400, 1000);
  tester.view.devicePixelRatio = 1.0;
  addTearDown(tester.view.reset);
  await tester.pumpWidget(MaterialApp(
    theme: buildAppTheme(),
    home: Scaffold(
        body: LogsPage(client: _stubClient(extraModels: extraModels), active: active)),
  ));
  await tester.pumpAndSettle();
}

void main() {
  testWidgets('初始展示全部条目,模型选项含日志里出现过的已删模型', (tester) async {
    await _pumpLogs(tester);
    expect(find.textContaining('runtime.us-east-1.kiro.dev'), findsOneWidget);
    expect(find.textContaining('resolve model=kimi-1'), findsOneWidget);
    expect(find.textContaining('GET /admin/logs'), findsOneWidget);
    expect(find.textContaining('session=abc'), findsOneWidget);

    await tester.tap(find.text('全部模型'));
    await tester.pumpAndSettle();
    // 配置里的两个模型 + 只在日志里出现的已删模型都可筛。
    expect(find.text('kiro-1/claude-sonnet-5'), findsOneWidget);
    expect(find.text('kimi-1/k3-256k'), findsOneWidget);
    expect(find.text('old-1/gone'), findsOneWidget);
  });

  testWidgets('按模型筛选:只留带该 model= 键的条目,无键条目一并隐藏', (tester) async {
    await _pumpLogs(tester);
    await tester.tap(find.text('全部模型'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('kiro-1/claude-sonnet-5'));
    await tester.pumpAndSettle();

    expect(find.textContaining('runtime.us-east-1.kiro.dev'), findsOneWidget);
    expect(find.textContaining('resolve model=kimi-1'), findsNothing);
    expect(find.textContaining('GET /admin/logs'), findsNothing);
    expect(find.textContaining('session=abc'), findsNothing);
  });

  testWidgets('按账号筛选:匹配 account= 键', (tester) async {
    await _pumpLogs(tester);
    await tester.tap(find.text('全部账号'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('kimi-1'));
    await tester.pumpAndSettle();

    expect(find.textContaining('resolve model=kimi-1'), findsOneWidget);
    expect(find.textContaining('runtime.us-east-1.kiro.dev'), findsNothing);
    expect(find.textContaining('GET /admin/logs'), findsNothing);
    expect(find.textContaining('session=abc'), findsNothing);
  });

  testWidgets('筛选与级别过滤叠加生效', (tester) async {
    await _pumpLogs(tester);
    await tester.tap(find.text('全部账号'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('old-1'));
    await tester.pumpAndSettle();
    // old-1 只出现在 chat 条目:级别再限 info 仍可见(条目即 info)。
    expect(find.textContaining('session=abc'), findsOneWidget);
    expect(find.textContaining('resolve model=kimi-1'), findsNothing);
  });

  testWidgets('按账号筛选:proxy 请求/响应行也带 account= 键,一并命中', (tester) async {
    await _pumpLogs(tester);
    await tester.tap(find.text('全部账号'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('kiro-1'));
    await tester.pumpAndSettle();

    // proxy 行的 account= 由源日志带上,账号过滤不再把它漏掉。
    expect(find.textContaining('runtime.us-east-1.kiro.dev'), findsOneWidget);
    expect(find.textContaining('resolve model=kimi-1'), findsNothing);
    expect(find.textContaining('GET /admin/logs'), findsNothing);
    expect(find.textContaining('session=abc'), findsNothing);
  });

  testWidgets('按来源筛选:种子源可选,只留该来源条目', (tester) async {
    await _pumpLogs(tester);
    await tester.tap(find.text('全部来源'));
    await tester.pumpAndSettle();
    // 种子源在空选项下也可选;resolve 是夹具里有的来源。
    expect(find.text('resolve'), findsWidgets);
    await tester.tap(find.text('resolve').last);
    await tester.pumpAndSettle();

    expect(find.textContaining('resolve model=kimi-1'), findsOneWidget);
    expect(find.textContaining('runtime.us-east-1.kiro.dev'), findsNothing);
    expect(find.textContaining('GET /admin/logs'), findsNothing);
    expect(find.textContaining('session=abc'), findsNothing);
  });

  testWidgets('重新激活时重拉配置列表,别处新建的模型出现在选项里', (tester) async {
    await _pumpLogs(tester);
    // 页常驻不重建:模拟别处新建了 ds-9/x 后切回本页(active 沿刷新)。
    // 不用 pumpAndSettle:active=true 启动的轮询周期表会让它永不收敛。
    await tester.pumpWidget(MaterialApp(
      theme: buildAppTheme(),
      home: Scaffold(
          body: LogsPage(
              client: _stubClient(extraModels: ['ds-9/x']), active: true)),
    ));
    await tester.pump();
    await tester.pump();
    await tester.tap(find.text('全部模型'));
    await tester.pump();
    await tester.pump();
    expect(find.text('ds-9/x'), findsOneWidget);
    // 收尾切回非激活停掉轮询表,避免测试结束悬挂 Timer 报错。
    await tester.pumpWidget(const SizedBox());
  });
}

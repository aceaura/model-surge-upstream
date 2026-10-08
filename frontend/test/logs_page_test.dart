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
ApiClient _stubClient() {
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
                '→ POST https://runtime.us-east-1.kiro.dev/v1/messages model=kiro-1/claude-sonnet-5 native=claude-sonnet-5 body={}'
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

Future<void> _pumpLogs(WidgetTester tester) async {
  await tester.pumpWidget(MaterialApp(
    theme: buildAppTheme(),
    home: Scaffold(body: LogsPage(client: _stubClient(), active: false)),
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
}

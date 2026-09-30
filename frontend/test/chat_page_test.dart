import 'dart:convert';

import 'package:flutter/gestures.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:msu_admin/api_client.dart';
import 'package:msu_admin/pages/chat_page.dart';
import 'package:msu_admin/theme.dart';

/// 会话/消息/模型的可变夹具：记录改名、删除、清除三类写操作。
class _Stub {
  final renamed = <Map<String, dynamic>>[];
  final deletedSessions = <String>[];
  final cleared = <String>[];

  final List<Map<String, dynamic>> sessions = [
    {
      'id': 's1',
      'title': '旧标题',
      'model_id': 'kimi-1/k2',
      'updated_at': '2026-09-16T10:00:00Z',
    },
    {
      'id': 's2',
      'title': '周报草稿',
      'model_id': '',
      'updated_at': '2026-09-17T10:00:00Z',
    },
  ];

  final Map<String, List<Map<String, dynamic>>> messages = {
    's1': [
      {
        'id': 1,
        'session_id': 's1',
        'role': 'user',
        'content': '你好',
        'created_at': '2026-09-16T10:00:00Z',
      },
    ],
    's2': [],
  };

  static const models = [
    {
      'id': 'kimi-1/k2',
      'account': 'kimi-1',
      'native_model': 'kimi-k2',
      'protocol': 'anthropic',
      'context_window': 0,
      'defaults': <String, dynamic>{},
      'overrides': <String, dynamic>{},
      'enabled': true,
    },
  ];

  Map<String, dynamic> _session(String id) =>
      sessions.firstWhere((s) => s['id'] == id);

  http.Response _json(Object? o) =>
      http.Response(jsonEncode(o), 200, headers: {'content-type': 'application/json'});

  Future<http.Response> handle(http.Request req) async {
    final p = req.url.path;
    if (req.method == 'GET' && p == '/admin/chat/sessions') {
      return _json({'sessions': sessions});
    }
    if (req.method == 'GET' && p == '/admin/models') {
      return _json({'models': models});
    }
    final msgs = RegExp(r'^/admin/chat/sessions/([^/]+)/messages$').firstMatch(p);
    if (msgs != null) {
      final id = msgs.group(1)!;
      if (req.method == 'GET') {
        return _json({'session': _session(id), 'messages': messages[id] ?? []});
      }
      if (req.method == 'DELETE') {
        cleared.add(id);
        messages[id] = [];
        return _json({});
      }
    }
    final sess = RegExp(r'^/admin/chat/sessions/([^/]+)$').firstMatch(p);
    if (sess != null) {
      final id = sess.group(1)!;
      if (req.method == 'PATCH') {
        final body = jsonDecode(utf8.decode(req.bodyBytes)) as Map<String, dynamic>;
        renamed.add(body);
        final s = Map<String, dynamic>.of(_session(id));
        s['title'] = body['title'];
        sessions[sessions.indexWhere((x) => x['id'] == id)] = s;
        return _json({'session': s});
      }
      if (req.method == 'DELETE') {
        deletedSessions.add(id);
        sessions.removeWhere((s) => s['id'] == id);
        return _json({});
      }
    }
    return http.Response('{"unexpected":"$p"}', 500);
  }
}

Future<_Stub> _pumpChat(WidgetTester tester) async {
  final stub = _Stub();
  await tester.pumpWidget(MaterialApp(
    theme: buildAppTheme(),
    home: Scaffold(
      body: ChatPage(
        client: ApiClient(
          baseUrl: 'http://127.0.0.1:8080',
          adminKey: 'adm',
          httpClient: MockClient(stub.handle),
        ),
      ),
    ),
  ));
  await tester.pumpAndSettle();
  return stub;
}

/// 会话行容器：标题文本往上找 InkWell(行点击层)。
Finder rowOf(String title) => find
    .ancestor(of: find.text(title), matching: find.byType(InkWell))
    .first;

void main() {
  testWidgets('selected row exposes rename: dialog prefills and PATCHes title',
      (tester) async {
    final stub = await _pumpChat(tester);

    // 选中行(首条)动作常显，直接点铅笔。
    await tester.tap(find.descendant(
        of: rowOf('旧标题'), matching: find.byTooltip('重命名')));
    await tester.pumpAndSettle();
    expect(find.text('重命名会话'), findsOneWidget);
    // 行标题 + 页头标题 + 弹窗预填输入框
    expect(find.text('旧标题'), findsAtLeastNWidgets(2));

    await tester.enterText(find.byType(TextFormField), '部署复盘');
    await tester.tap(find.widgetWithText(FilledButton, '保存'));
    await tester.pumpAndSettle();

    expect(stub.renamed, [
      {'title': '部署复盘'}
    ]);
    expect(find.text('部署复盘'), findsAtLeastNWidgets(1));
    expect(find.text('旧标题'), findsNothing);
  });

  testWidgets('rename rejects empty title', (tester) async {
    await _pumpChat(tester);
    await tester.tap(find.descendant(
        of: rowOf('旧标题'), matching: find.byTooltip('重命名')));
    await tester.pumpAndSettle();

    await tester.enterText(find.byType(TextFormField), '   ');
    await tester.tap(find.widgetWithText(FilledButton, '保存'));
    await tester.pump();
    expect(find.text('标题不能为空'), findsOneWidget);
    expect(find.text('重命名会话'), findsOneWidget, reason: '校验失败弹窗不关');
  });

  testWidgets('unselected row hides actions until hovered', (tester) async {
    final stub = await _pumpChat(tester);

    // 未悬停：删除图标在树里但被 IgnorePointer 屏蔽，点了只切行不弹窗。
    await tester.tap(
        find.descendant(
            of: rowOf('周报草稿'), matching: find.byTooltip('删除会话')),
        warnIfMissed: false);
    await tester.pumpAndSettle();
    expect(find.text('删除会话'), findsNothing);
    expect(stub.deletedSessions, isEmpty);

    // 悬停后动作显现，垃圾桶可用。
    final gesture = await tester.createGesture(kind: PointerDeviceKind.mouse);
    await gesture.addPointer(location: Offset.zero);
    await tester.pump();
    await gesture.moveTo(tester.getCenter(rowOf('周报草稿')));
    await tester.pumpAndSettle();

    await tester.tap(find.descendant(
        of: rowOf('周报草稿'), matching: find.byTooltip('删除会话')));
    await tester.pumpAndSettle();
    expect(find.text('删除会话'), findsOneWidget);

    await tester.tap(find.widgetWithText(FilledButton, '删除'));
    await tester.pumpAndSettle();
    expect(stub.deletedSessions, ['s2']);
    expect(find.text('周报草稿'), findsNothing);
    await gesture.removePointer();
  });

  testWidgets('clear button clears current session messages', (tester) async {
    final stub = await _pumpChat(tester);

    // s1 有消息：清除可用。
    final clear = find.widgetWithText(OutlinedButton, '清除');
    expect(tester.widget<OutlinedButton>(clear).onPressed, isNotNull);
    expect(find.text('你好'), findsOneWidget);

    await tester.tap(clear);
    await tester.pumpAndSettle();
    expect(find.text('清除消息'), findsOneWidget);
    await tester.tap(find.widgetWithText(FilledButton, '清除'));
    await tester.pumpAndSettle();

    expect(stub.cleared, ['s1']);
    expect(find.text('你好'), findsNothing);
    expect(tester.widget<OutlinedButton>(clear).onPressed, isNull,
        reason: '清空后按钮回到禁用');
  });

  testWidgets('clear button disabled for session without messages',
      (tester) async {
    await _pumpChat(tester);
    await tester.tap(rowOf('周报草稿'));
    await tester.pumpAndSettle();
    expect(
      tester.widget<OutlinedButton>(find.widgetWithText(OutlinedButton, '清除'))
          .onPressed,
      isNull,
    );
  });
}

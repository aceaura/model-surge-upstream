import 'dart:async';
import 'dart:convert';

import 'package:flutter/gestures.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:msu_admin/api_client.dart';
import 'package:msu_admin/attachments/image_source.dart';
import 'package:msu_admin/pages/chat_page.dart';
import 'package:msu_admin/theme.dart';
import 'package:msu_admin/ui/styled_dropdown.dart';

/// 1×1 透明 PNG：气泡/缩略图渲染断言用，解码必须能真过。
const kTinyPng =
    'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk'
    '+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==';

/// 取图 fake：插件在 flutter test 里不能真调。
class _FakeImageSource implements ImageSource {
  _FakeImageSource({this.picked = const [], this.clipboard});

  final List<PendingImage> picked;
  final PendingImage? clipboard;

  @override
  Future<List<PendingImage>> pickFiles() async => picked;

  @override
  Future<PendingImage?> readClipboard() async => clipboard;
}

PendingImage _tinyPending() =>
    PendingImage(mime: 'image/png', bytes: base64Decode(kTinyPng));

/// 会话/消息/模型的可变夹具：记录改名、删除、清除、发送四类写操作。
class _Stub {
  final renamed = <Map<String, dynamic>>[];
  final deletedSessions = <String>[];
  final cleared = <String>[];
  final sent = <Map<String, dynamic>>[];
  bool failSend = false;

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
      {
        'id': 2,
        'session_id': 's1',
        'role': 'user',
        'content': '看图',
        'attachments': [
          {'mime': 'image/png', 'data': kTinyPng},
        ],
        'created_at': '2026-09-16T10:01:00Z',
      },
      {
        'id': 3,
        'session_id': 's1',
        'role': 'assistant',
        'content':
            '改请求头试试：\n```bash\ncurl -H "Authorization: Bearer sk-x" https://up.example\n```',
        'created_at': '2026-09-16T10:01:30Z',
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
      if (req.method == 'POST') {
        final body =
            jsonDecode(utf8.decode(req.bodyBytes)) as Map<String, dynamic>;
        sent.add(body);
        if (failSend) {
          return http.Response(
            jsonEncode({
              'error': {
                'code': 'upstream_unavailable',
                'message': '上游返回 HTTP 502：bad gateway',
                'status': 502,
              },
            }),
            502,
            headers: {'content-type': 'application/json'},
          );
        }
        final list = messages[id] ??= [];
        list.add({
          'id': list.length + 1,
          'session_id': id,
          'role': 'user',
          'content': body['content'],
          if (body['images'] != null) 'attachments': body['images'],
          'created_at': '2026-10-01T08:23:00Z',
        });
        list.add({
          'id': list.length + 1,
          'session_id': id,
          'role': 'assistant',
          'content': '收到',
          'created_at': '2026-10-01T08:23:05Z',
        });
        return _json({'messages': list});
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

Future<_Stub> _pumpChat(WidgetTester tester, {ImageSource? imageSource}) async {
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
        imageSource: imageSource,
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

  testWidgets('user message with attachments renders image in bubble',
      (tester) async {
    await _pumpChat(tester);
    // s1 第二条消息带一张图：气泡里出现 Image，文本仍在。
    expect(find.byType(Image), findsOneWidget);
    expect(find.text('看图'), findsOneWidget);
  });

  testWidgets('assistant code fence renders code block with copy entry',
      (tester) async {
    await _pumpChat(tester);
    expect(find.text('bash'), findsOneWidget, reason: '代码块头行语言标签');
    expect(find.byTooltip('复制'), findsOneWidget);
    expect(find.textContaining('Authorization'), findsOneWidget);
  });

  testWidgets('paperclip adds thumbnail strip and send posts images',
      (tester) async {
    final stub = await _pumpChat(tester,
        imageSource: _FakeImageSource(picked: [_tinyPending()]));

    await tester.tap(find.byTooltip('添加图片（也可 Ctrl+V 粘贴截图）'));
    await tester.pumpAndSettle();
    // 气泡原有 1 张 + strip 新增 1 张缩略图；提示行出现。
    expect(find.byType(Image), findsNWidgets(2));
    expect(find.text('图片需模型支持视觉输入，单张不超过 4 MB，最多 4 张'),
        findsOneWidget);

    await tester.enterText(find.byType(TextField), '这张报错帮我看看');
    await tester.tap(find.byIcon(Icons.send_rounded));
    await tester.pumpAndSettle();

    expect(stub.sent.single['content'], '这张报错帮我看看');
    expect(stub.sent.single['images'], [
      {'mime': 'image/png', 'data': kTinyPng},
    ]);
    expect(find.text('收到'), findsOneWidget, reason: '助手回复落屏');
    expect(find.text('图片需模型支持视觉输入，单张不超过 4 MB，最多 4 张'), findsNothing,
        reason: '发送成功后 strip 清空');
  });

  testWidgets('hover thumbnail reveals remove button and it removes',
      (tester) async {
    await _pumpChat(tester,
        imageSource: _FakeImageSource(picked: [_tinyPending()]));
    await tester.tap(find.byTooltip('添加图片（也可 Ctrl+V 粘贴截图）'));
    await tester.pumpAndSettle();
    expect(find.byType(Image), findsNWidgets(2));

    // 悬停缩略图让移除钮可点（Opacity+IgnorePointer 手法）。
    final gesture = await tester.createGesture(kind: PointerDeviceKind.mouse);
    await gesture.addPointer(location: Offset.zero);
    await tester.pump();
    final thumbStack = find.ancestor(
        of: find.byIcon(Icons.close), matching: find.byType(Stack));
    await gesture.moveTo(tester.getCenter(thumbStack.first));
    await tester.pumpAndSettle();
    await tester.tap(find.byIcon(Icons.close));
    await tester.pumpAndSettle();

    expect(find.byType(Image), findsOneWidget, reason: 'strip 只剩气泡图');
    expect(find.text('图片需模型支持视觉输入，单张不超过 4 MB，最多 4 张'), findsNothing);
    await gesture.removePointer();
  });

  testWidgets('send failure restores text and pending images', (tester) async {
    final stub = await _pumpChat(tester,
        imageSource: _FakeImageSource(picked: [_tinyPending()]));
    stub.failSend = true;

    await tester.tap(find.byTooltip('添加图片（也可 Ctrl+V 粘贴截图）'));
    await tester.pumpAndSettle();
    await tester.enterText(find.byType(TextField), '看这张');
    await tester.tap(find.byIcon(Icons.send_rounded));
    await tester.pumpAndSettle();

    expect(find.text('看这张'), findsOneWidget, reason: '失败时文本还回输入框');
    expect(find.byType(Image), findsNWidgets(2), reason: '附件也还回 strip');
    expect(find.text('图片需模型支持视觉输入，单张不超过 4 MB，最多 4 张'), findsOneWidget);

    // 冲刷失败提示框的 2.4s 驻留定时器与滑出动画,避免遗留 Timer。
    await tester.pump(const Duration(seconds: 3));
    await tester.pumpAndSettle();
  });

  testWidgets('发送等待期间输入框提示切换为思考中,回复后还原', (tester) async {
    // codex 非流式等待可达几十秒:输入框清空后必须给出等待反馈。
    final gate = Completer<void>();
    final client = ApiClient(
      baseUrl: 'http://127.0.0.1:8080',
      adminKey: 'adm',
      httpClient: MockClient((request) async {
        final p = request.url.path;
        Object? payload;
        if (request.method == 'GET' && p == '/admin/chat/sessions') {
          payload = {
            'sessions': [
              {
                'id': 's1',
                'title': '旧对话',
                'model_id': 'kimi-1/k2',
                'updated_at': '2026-09-16T10:00:00Z',
              },
            ],
          };
        } else if (request.method == 'GET' && p == '/admin/models') {
          payload = {'models': _Stub.models};
        } else if (request.method == 'GET' &&
            p == '/admin/chat/sessions/s1/messages') {
          payload = {
            'session': {
              'id': 's1',
              'title': '旧对话',
              'model_id': 'kimi-1/k2',
              'updated_at': '2026-09-16T10:00:00Z',
            },
            'messages': <dynamic>[],
          };
        } else if (request.method == 'POST' &&
            p == '/admin/chat/sessions/s1/messages') {
          await gate.future;
          payload = {
            'messages': [
              {
                'id': 1,
                'session_id': 's1',
                'role': 'user',
                'content': '你好',
                'created_at': '2026-10-03T18:00:00Z',
              },
              {
                'id': 2,
                'session_id': 's1',
                'role': 'assistant',
                'content': '在的',
                'created_at': '2026-10-03T18:00:05Z',
              },
            ],
          };
        } else {
          payload = {};
        }
        return http.Response(jsonEncode(payload), 200,
            headers: {'content-type': 'application/json'});
      }),
    );
    await tester.pumpWidget(MaterialApp(
      theme: buildAppTheme(),
      home: Scaffold(
        body: ChatPage(client: client, imageSource: _FakeImageSource()),
      ),
    ));
    await tester.pumpAndSettle();

    await tester.enterText(find.byType(TextField), '你好');
    await tester.tap(find.byIcon(Icons.send_rounded));
    await tester.pump(); // 进入 _sending,回复未归

    expect(find.text('正在思考，请稍候…'), findsOneWidget);
    expect(find.text('输入消息… Enter 发送，Shift+Enter 换行'), findsNothing);

    gate.complete();
    await tester.pumpAndSettle();
    expect(find.text('输入消息… Enter 发送，Shift+Enter 换行'), findsOneWidget,
        reason: '回复落屏后提示还原');
    expect(find.text('正在思考，请稍候…'), findsNothing);
  });

  testWidgets('发送后用户消息立即上屏,不等回复返回', (tester) async {
    // codex 非流式整轮等待几十秒:此前用户消息要等 Send 返回才出现,
    // 等待期间右侧只有思考气泡,像消息没发出去(用户截图点名)。
    final gate = Completer<void>();
    final client = ApiClient(
      baseUrl: 'http://127.0.0.1:8080',
      adminKey: 'adm',
      httpClient: MockClient((request) async {
        final p = request.url.path;
        Object? payload;
        if (request.method == 'GET' && p == '/admin/chat/sessions') {
          payload = {
            'sessions': [
              {
                'id': 's1',
                'title': '旧对话',
                'model_id': 'kimi-1/k2',
                'updated_at': '2026-09-16T10:00:00Z',
              },
            ],
          };
        } else if (request.method == 'GET' && p == '/admin/models') {
          payload = {'models': _Stub.models};
        } else if (request.method == 'GET' &&
            p == '/admin/chat/sessions/s1/messages') {
          payload = {
            'session': {
              'id': 's1',
              'title': '旧对话',
              'model_id': 'kimi-1/k2',
              'updated_at': '2026-09-16T10:00:00Z',
            },
            'messages': <dynamic>[],
          };
        } else if (request.method == 'POST' &&
            p == '/admin/chat/sessions/s1/messages') {
          await gate.future;
          payload = {
            'messages': [
              {
                'id': 1,
                'session_id': 's1',
                'role': 'user',
                'content': '7×8=?',
                'created_at': '2026-10-03T18:00:00Z',
              },
              {
                'id': 2,
                'session_id': 's1',
                'role': 'assistant',
                'content': '56',
                'created_at': '2026-10-03T18:00:05Z',
              },
            ],
          };
        } else {
          payload = {};
        }
        return http.Response(jsonEncode(payload), 200,
            headers: {'content-type': 'application/json'});
      }),
    );
    await tester.pumpWidget(MaterialApp(
      theme: buildAppTheme(),
      home: Scaffold(
        body: ChatPage(client: client, imageSource: _FakeImageSource()),
      ),
    ));
    await tester.pumpAndSettle();

    await tester.enterText(find.byType(TextField), '7×8=?');
    await tester.tap(find.byIcon(Icons.send_rounded));
    await tester.pump(); // 进入 _sending,回复未归

    expect(find.text('7×8=?'), findsOneWidget,
        reason: '等待回复期间用户消息已乐观上屏');
    expect(find.text('思考中…'), findsOneWidget);

    gate.complete();
    await tester.pumpAndSettle();
    expect(find.text('7×8=?'), findsOneWidget,
        reason: '整轮返回后以服务端落库版替换,不重复');
    expect(find.text('56'), findsOneWidget);
  });

  testWidgets('重新激活(active false→true)静默重拉模型与会话,选择器能看到新建模型',
      (tester) async {
    // 页在 IndexedStack 里常驻:别处(模型页/API)新建模型后切回对话页,
    // 必须重拉模型列表,否则选择器里永远没有新模型。
    var modelsCalls = 0;
    var sessionsCalls = 0;
    final models = <Map<String, dynamic>>[
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
    final client = ApiClient(
      baseUrl: 'http://127.0.0.1:8080',
      adminKey: 'adm',
      httpClient: MockClient((request) async {
        final p = request.url.path;
        Object? payload;
        if (request.method == 'GET' && p == '/admin/chat/sessions') {
          sessionsCalls++;
          payload = {
            'sessions': [
              {
                'id': 's1',
                'title': '旧对话',
                'model_id': 'kimi-1/k2',
                'updated_at': '2026-09-16T10:00:00Z',
              },
            ],
          };
        } else if (request.method == 'GET' && p == '/admin/models') {
          modelsCalls++;
          payload = {'models': models};
        } else if (p == '/admin/chat/sessions/s1/messages') {
          payload = {
            'session': {
              'id': 's1',
              'title': '旧对话',
              'model_id': 'kimi-1/k2',
              'updated_at': '2026-09-16T10:00:00Z',
            },
            'messages': <dynamic>[],
          };
        } else {
          payload = {};
        }
        return http.Response(jsonEncode(payload), 200,
            headers: {'content-type': 'application/json'});
      }),
    );

    Widget host(bool active) => MaterialApp(
          theme: buildAppTheme(),
          home: Scaffold(
            body: ChatPage(
              client: client,
              active: active,
              imageSource: _FakeImageSource(),
            ),
          ),
        );

    await tester.pumpWidget(host(false));
    await tester.pumpAndSettle();
    expect(modelsCalls, 1, reason: '首载一次');
    expect(sessionsCalls, 1);

    await tester.pumpWidget(host(false));
    await tester.pumpAndSettle();
    expect(modelsCalls, 1, reason: '保持后台不重拉');
    expect(sessionsCalls, 1);

    // 别处新建了模型,切回对话页。
    models.add({
      'id': 'codex-1/gpt-6.1-sol',
      'account': 'codex-1',
      'native_model': 'gpt-6.1-sol',
      'protocol': 'responses',
      'context_window': 0,
      'defaults': <String, dynamic>{},
      'overrides': <String, dynamic>{},
      'enabled': true,
    });
    await tester.pumpWidget(host(true));
    await tester.pumpAndSettle();
    expect(modelsCalls, 2, reason: '激活沿重拉一次');
    expect(sessionsCalls, 2);

    // 新模型进了选择器选项。
    await tester.tap(find.byType(StyledDropdown));
    await tester.pumpAndSettle();
    expect(find.text('codex-1/gpt-6.1-sol'), findsOneWidget);
  });

  testWidgets('推理档选择器按所选模型的支持列表渲染,选择后随发送上行', (tester) async {
    // kimi-1/k2 有效支持列表为空:选择器不露面;切到 codex-1/gpt-6.1-sol
    // (有效列表 minimal~high)出现且只渲染这些档,选「高」后 POST body 带
    // effort=high。
    final sent = <Map<String, dynamic>>[];
    final client = ApiClient(
      baseUrl: 'http://127.0.0.1:8080',
      adminKey: 'adm',
      httpClient: MockClient((request) async {
        final p = request.url.path;
        Object? payload;
        if (request.method == 'GET' && p == '/admin/chat/sessions') {
          payload = {
            'sessions': [
              {
                'id': 's1',
                'title': '旧对话',
                'model_id': 'kimi-1/k2',
                'updated_at': '2026-09-16T10:00:00Z',
              },
            ],
          };
        } else if (request.method == 'GET' && p == '/admin/models') {
          payload = {
            'models': [
              ..._Stub.models,
              {
                'id': 'codex-1/gpt-6.1-sol',
                'account': 'codex-1',
                'native_model': 'gpt-6.1-sol',
                'protocol': 'responses',
                'context_window': 0,
                'defaults': <String, dynamic>{},
                'overrides': <String, dynamic>{},
                'efforts': null,
                'efforts_effective': ['minimal', 'low', 'medium', 'high'],
                'enabled': true,
              },
            ],
          };
        } else if (request.method == 'GET' &&
            p == '/admin/chat/sessions/s1/messages') {
          payload = {
            'session': {
              'id': 's1',
              'title': '旧对话',
              'model_id': 'kimi-1/k2',
              'updated_at': '2026-09-16T10:00:00Z',
            },
            'messages': <dynamic>[],
          };
        } else if (request.method == 'POST' &&
            p == '/admin/chat/sessions/s1/messages') {
          sent.add(jsonDecode(utf8.decode(request.bodyBytes)));
          payload = {
            'messages': [
              {
                'id': 1,
                'session_id': 's1',
                'role': 'user',
                'content': 'hi',
                'created_at': '2026-10-03T18:00:00Z',
              },
              {
                'id': 2,
                'session_id': 's1',
                'role': 'assistant',
                'content': 'ok',
                'created_at': '2026-10-03T18:00:05Z',
              },
            ],
          };
        } else {
          payload = {};
        }
        return http.Response(jsonEncode(payload), 200,
            headers: {'content-type': 'application/json'});
      }),
    );
    await tester.pumpWidget(MaterialApp(
      theme: buildAppTheme(),
      home: Scaffold(
        body: ChatPage(client: client, imageSource: _FakeImageSource()),
      ),
    ));
    await tester.pumpAndSettle();

    // 会话回显 kimi-1/k2(支持列表为空):推理档选择器不出现。
    expect(find.text('默认'), findsNothing);

    // 模型切到 codex-1/gpt-6.1-sol:选择器出现,只渲染该模型的支持档。
    await tester.tap(find.text('kimi-1/k2'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('codex-1/gpt-6.1-sol'));
    await tester.pumpAndSettle();
    expect(find.text('默认'), findsOneWidget);

    await tester.tap(find.text('默认'));
    await tester.pumpAndSettle();
    expect(find.text('最小'), findsOneWidget);
    expect(find.text('超高'), findsNothing, reason: '词表有但模型不支持的不渲染');

    // 选「高」并发送。
    await tester.tap(find.text('高'));
    await tester.pumpAndSettle();
    await tester.enterText(find.byType(TextField), 'hi');
    await tester.tap(find.byIcon(Icons.send_rounded));
    await tester.pumpAndSettle();

    expect(sent.single['model_id'], 'codex-1/gpt-6.1-sol');
    expect(sent.single['effort'], 'high');
  });
}

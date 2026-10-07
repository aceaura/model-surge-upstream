import 'dart:async';
import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:msu_admin/api_client.dart';
import 'package:msu_admin/models.dart';
import 'package:msu_admin/theme.dart';
import 'package:msu_admin/ui/upstream_model_picker.dart';

Finder _key(String name) => find.byKey(ValueKey('upstream-model-$name'));
Finder _option(String id) => _key('option-$id');

/// 组件测试使用 MockClient，但不猜测待集成 API 的路由或 JSON 命名。
/// 本地测试传输格式只负责产出约定的两种 DTO；实际 API 另做集成测试。
class _PickerClient extends ApiClient {
  _PickerClient(this.transport)
    : super(
        baseUrl: 'https://picker.test',
        adminKey: 'test-only',
        httpClient: transport,
      );

  final MockClient transport;

  @override
  Future<UpstreamModelListing> listUpstreamModels(String account) async {
    final response = await transport.get(
      Uri.https('picker.test', '/fixture', {'account': account}),
    );
    if (response.statusCode != 200) throw StateError('Fixture failure');
    final body = jsonDecode(response.body) as Map<String, dynamic>;
    return UpstreamModelListing(
      queryable: body['queryable'] as bool,
      models: (body['models'] as List<dynamic>).map((entry) {
        final model = entry as Map<String, dynamic>;
        return UpstreamModelOption(
          id: model['id'] as String,
          displayName: model['displayName'] as String? ?? '',
        );
      }).toList(),
    );
  }
}

http.Response _reply(
  List<String> ids, {
  bool queryable = true,
  Map<String, String> names = const {},
}) {
  return http.Response(
    jsonEncode({
      'queryable': queryable,
      'models': [
        for (final id in ids) {'id': id, 'displayName': names[id] ?? ''},
      ],
    }),
    200,
    headers: {'content-type': 'application/json; charset=utf-8'},
  );
}

_PickerClient _client(Future<http.Response> Function(http.Request) handler) {
  final client = _PickerClient(MockClient(handler));
  addTearDown(client.close);
  return client;
}

class _Harness extends StatefulWidget {
  const _Harness({
    super.key,
    required this.client,
    this.dark = false,
    this.alignment = Alignment.topCenter,
    this.width = 640,
  });

  final ApiClient client;
  final bool dark;
  final Alignment alignment;
  final double width;

  @override
  State<_Harness> createState() => _HarnessState();
}

class _HarnessState extends State<_Harness> {
  final controller = TextEditingController(text: 'manual-model');
  final selected = <String>[];
  late ApiClient client = widget.client;
  String account = 'account-a';
  String value = 'manual-model';
  bool enabled = true;
  bool visible = true;

  void update({
    String? newAccount,
    String? newValue,
    ApiClient? newClient,
    bool? newEnabled,
    bool? newVisible,
  }) {
    setState(() {
      if (newAccount != null) account = newAccount;
      if (newClient != null) client = newClient;
      if (newValue != null) {
        value = newValue;
        controller.text = newValue;
      }
      if (newEnabled != null) enabled = newEnabled;
      if (newVisible != null) visible = newVisible;
    });
  }

  @override
  void dispose() {
    controller.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      theme: widget.dark ? buildAppDarkTheme() : buildAppTheme(),
      home: Scaffold(
        body: Align(
          alignment: widget.alignment,
          child: Padding(
            padding: const EdgeInsets.all(16),
            child: SizedBox(
              width: widget.width,
              child: Column(
                mainAxisSize: MainAxisSize.min,
                children: [
                  if (visible)
                    UpstreamModelPicker(
                      client: client,
                      account: account,
                      value: value,
                      enabled: enabled,
                      onSelected: (id) {
                        selected.add(id);
                        update(newValue: id);
                      },
                      child: TextFormField(
                        key: const ValueKey('manual-input'),
                        controller: controller,
                        onChanged: (text) => setState(() => value = text),
                      ),
                    ),
                  const SizedBox(height: 8),
                  const SizedBox(
                    key: ValueKey('unchanged-card'),
                    height: 32,
                    width: double.infinity,
                    child: Text('下方卡片保持不动'),
                  ),
                ],
              ),
            ),
          ),
        ),
      ),
    );
  }
}

Future<GlobalKey<_HarnessState>> _pump(
  WidgetTester tester,
  ApiClient client, {
  bool dark = false,
  Alignment alignment = Alignment.topCenter,
  Size size = const Size(900, 700),
  double width = 640,
}) async {
  tester.view.physicalSize = size;
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.reset);
  final key = GlobalKey<_HarnessState>();
  await tester.pumpWidget(
    _Harness(
      key: key,
      client: client,
      dark: dark,
      alignment: alignment,
      width: width,
    ),
  );
  await tester.pumpAndSettle();
  return key;
}

Future<void> _open(WidgetTester tester) async {
  await tester.tap(_key('fetch'));
  await tester.pumpAndSettle();
  // 搜索焦点在结果构建后的 frame callback 中申请。
  await tester.pump();
}

void main() {
  testWidgets(
    'fetch, search both fields case-insensitively, clear, select ID',
    (tester) async {
      var calls = 0;
      final client = _client((request) async {
        ++calls;
        expect(request.url.queryParameters['account'], 'account-a');
        return _reply(
          ['manual-model', 'QWEN-Max', 'claude-sonnet'],
          names: {'claude-sonnet': 'Friendly Sonnet'},
        );
      });
      final harness = await _pump(tester, client);
      final before = tester.getRect(
        find.byKey(const ValueKey('unchanged-card')),
      );
      final inputSize = tester.getSize(
        find.byKey(const ValueKey('manual-input')),
      );
      expect(tester.getSize(_key('fetch')).height, inputSize.height);
      expect(inputSize.height, closeTo(48, 1));

      await _open(tester);
      expect(calls, 1);
      expect(find.text('选择上游模型'), findsOneWidget);
      expect(find.text('account-a · 3 个模型'), findsOneWidget);
      expect(find.text('共 3 个模型 · 点击即填入'), findsOneWidget);
      expect(
        find.descendant(
          of: _option('manual-model'),
          matching: find.byIcon(Icons.check_rounded),
        ),
        findsOneWidget,
      );
      expect(
        tester.getRect(find.byKey(const ValueKey('unchanged-card'))),
        before,
      );
      final panel = tester.getRect(_key('panel'));
      expect(panel.width, lessThanOrEqualTo(520));
      expect(panel.right, tester.getRect(_key('fetch')).right);

      await tester.enterText(_key('search'), 'qwen');
      await tester.pump();
      expect(_option('QWEN-Max'), findsOneWidget);
      expect(_option('claude-sonnet'), findsNothing);
      expect(find.text('找到 1 / 3 个模型'), findsOneWidget);
      await tester.tap(_key('clear'));
      await tester.pump();
      expect(_option('manual-model'), findsOneWidget);
      await tester.enterText(_key('search'), 'FRIENDLY');
      await tester.pump();
      expect(_option('claude-sonnet'), findsOneWidget);
      await tester.tap(_option('claude-sonnet'));
      await tester.pumpAndSettle();
      expect(_key('panel'), findsNothing);
      expect(harness.currentState!.selected, ['claude-sonnet']);
      expect(harness.currentState!.controller.text, 'claude-sonnet');

      await _open(tester);
      expect(calls, 2, reason: '每次获取均调用接口，不使用组件本地缓存');
      await tester.tap(_key('refresh'));
      await tester.pumpAndSettle();
      expect(calls, 3);
    },
  );

  testWidgets('keyboard down/up/Enter selects the highlighted ID', (
    tester,
  ) async {
    final harness = await _pump(
      tester,
      _client((_) async => _reply(['one', 'two', 'three'])),
    );
    await _open(tester);
    await tester.sendKeyEvent(LogicalKeyboardKey.arrowDown);
    await tester.sendKeyEvent(LogicalKeyboardKey.arrowDown);
    await tester.sendKeyEvent(LogicalKeyboardKey.arrowUp);
    await tester.sendKeyEvent(LogicalKeyboardKey.arrowDown);
    await tester.sendKeyEvent(LogicalKeyboardKey.enter);
    await tester.pumpAndSettle();
    expect(harness.currentState!.selected, ['two']);
    expect(_key('panel'), findsNothing);
  });

  testWidgets('Enter without arrow navigation selects first filtered ID', (
    tester,
  ) async {
    final harness = await _pump(
      tester,
      _client((_) async => _reply(['one', 'two'])),
    );
    await _open(tester);
    await tester.enterText(_key('search'), 'two');
    await tester.sendKeyEvent(LogicalKeyboardKey.enter);
    await tester.pumpAndSettle();
    expect(harness.currentState!.selected, ['two']);
  });

  testWidgets('long list uses lazy rows, scrollbar and keyboard scrolling', (
    tester,
  ) async {
    final ids = List.generate(150, (index) => 'model-$index');
    final harness = await _pump(tester, _client((_) async => _reply(ids)));
    await _open(tester);
    expect(
      find.descendant(of: _key('panel'), matching: find.byType(Scrollbar)),
      findsOneWidget,
    );
    expect(_option('model-149'), findsNothing);
    for (var index = 0; index < 25; ++index) {
      await tester.sendKeyEvent(LogicalKeyboardKey.arrowDown);
      await tester.pump();
    }
    expect(_option('model-24'), findsOneWidget);
    await tester.sendKeyEvent(LogicalKeyboardKey.enter);
    await tester.pumpAndSettle();
    expect(harness.currentState!.selected, ['model-24']);

    await _open(tester);
    final scrollable = find.descendant(
      of: _key('list'),
      matching: find.byType(Scrollable),
    );
    await tester.scrollUntilVisible(
      _option('model-149'),
      250,
      scrollable: scrollable,
      maxScrolls: 50,
    );
    await tester.tap(_option('model-149'));
    await tester.pumpAndSettle();
    expect(harness.currentState!.selected.last, 'model-149');
  });

  testWidgets(
    'loading prevents duplicates and close invalidates late response',
    (tester) async {
      final pending = Completer<http.Response>();
      var calls = 0;
      final client = _client((_) {
        ++calls;
        return pending.future;
      });
      final harness = await _pump(tester, client);
      await tester.tap(_key('fetch'));
      await tester.pump();
      expect(find.text('正在获取模型列表'), findsOneWidget);
      expect(tester.widget<OutlinedButton>(_key('fetch')).onPressed, isNull);
      expect(tester.widget<IconButton>(_key('refresh')).onPressed, isNull);
      await tester.tap(_key('fetch'));
      await tester.pump();
      expect(calls, 1);
      await tester.tap(_key('close'));
      await tester.pump();
      expect(_key('panel'), findsNothing);
      pending.complete(_reply(['stale']));
      await tester.pumpAndSettle();
      expect(_key('panel'), findsNothing);
      expect(harness.currentState!.controller.text, 'manual-model');
      expect(harness.currentState!.selected, isEmpty);
      expect(tester.takeException(), isNull);
    },
  );

  testWidgets('closing then reopening ignores older result and error', (
    tester,
  ) async {
    final old = Completer<http.Response>();
    var calls = 0;
    final harness = await _pump(
      tester,
      _client((_) {
        ++calls;
        return calls == 1 ? old.future : Future.value(_reply(['fresh']));
      }),
    );
    await tester.tap(_key('fetch'));
    await tester.pump();
    await tester.tap(_key('close'));
    await tester.pump();
    await _open(tester);
    expect(_option('fresh'), findsOneWidget);
    old.complete(http.Response('old failure', 500));
    await tester.pumpAndSettle();
    expect(_option('fresh'), findsOneWidget);
    expect(find.text('获取模型列表失败'), findsNothing);
    expect(harness.currentState!.selected, isEmpty);
  });

  testWidgets('unmounting while loading removes portal and ignores response', (
    tester,
  ) async {
    final pending = Completer<http.Response>();
    final client = _client((_) => pending.future);
    await _pump(tester, client);
    await tester.tap(_key('fetch'));
    await tester.pump();
    await tester.pumpWidget(const SizedBox.shrink());
    pending.complete(_reply(['late']));
    await tester.pumpAndSettle();
    expect(_key('panel'), findsNothing);
    expect(tester.takeException(), isNull);
  });

  testWidgets('account change closes and cannot mix delayed old account data', (
    tester,
  ) async {
    final old = Completer<http.Response>();
    final accounts = <String>[];
    final harness = await _pump(
      tester,
      _client((request) {
        final account = request.url.queryParameters['account']!;
        accounts.add(account);
        return account == 'account-a'
            ? old.future
            : Future.value(_reply(['new-account-model']));
      }),
    );
    await tester.tap(_key('fetch'));
    await tester.pump();
    harness.currentState!.update(newAccount: 'account-b');
    await tester.pump();
    expect(_key('panel'), findsNothing);
    await _open(tester);
    old.complete(_reply(['wrong-account-model']));
    await tester.pumpAndSettle();
    expect(accounts, ['account-a', 'account-b']);
    expect(_option('wrong-account-model'), findsNothing);
    expect(_option('new-account-model'), findsOneWidget);
    expect(find.text('account-b · 1 个模型'), findsOneWidget);
    expect(harness.currentState!.controller.text, 'manual-model');
  });

  testWidgets('client replacement invalidates in-flight request', (
    tester,
  ) async {
    final old = Completer<http.Response>();
    final harness = await _pump(tester, _client((_) => old.future));
    await tester.tap(_key('fetch'));
    await tester.pump();
    harness.currentState!.update(
      newClient: _client((_) async => _reply(['replacement'])),
    );
    await tester.pump();
    expect(_key('panel'), findsNothing);
    await _open(tester);
    old.complete(_reply(['old-client']));
    await tester.pumpAndSettle();
    expect(_option('replacement'), findsOneWidget);
    expect(_option('old-client'), findsNothing);
    expect(tester.takeException(), isNull);
  });

  testWidgets('external value update moves check without firing callback', (
    tester,
  ) async {
    final harness = await _pump(
      tester,
      _client((_) async => _reply(['one', 'two'])),
    );
    await _open(tester);
    harness.currentState!.update(newValue: 'two');
    await tester.pump();
    expect(
      find.descendant(
        of: _option('two'),
        matching: find.byIcon(Icons.check_rounded),
      ),
      findsOneWidget,
    );
    expect(
      find.descendant(
        of: _option('one'),
        matching: find.byIcon(Icons.check_rounded),
      ),
      findsNothing,
    );
    expect(harness.currentState!.selected, isEmpty);
  });

  testWidgets('failure preserves manual input and supports retry', (
    tester,
  ) async {
    var calls = 0;
    final harness = await _pump(
      tester,
      _client((_) async {
        ++calls;
        return calls == 1
            ? http.Response('failure', 503)
            : _reply(['recovered']);
      }),
    );
    await _open(tester);
    expect(find.text('获取模型列表失败'), findsOneWidget);
    expect(find.text('原有模型名未更改'), findsOneWidget);
    expect(harness.currentState!.controller.text, 'manual-model');
    expect(harness.currentState!.selected, isEmpty);
    await tester.tap(find.widgetWithText(OutlinedButton, '重新获取'));
    await tester.pumpAndSettle();
    expect(calls, 2);
    expect(_option('recovered'), findsOneWidget);
    await tester.tap(_option('recovered'));
    await tester.pumpAndSettle();
    expect(harness.currentState!.selected, ['recovered']);
  });

  testWidgets('not queryable, empty listing and no matches remain distinct', (
    tester,
  ) async {
    var calls = 0;
    final harness = await _pump(
      tester,
      _client((_) async {
        ++calls;
        if (calls == 1) return _reply(['ignored'], queryable: false);
        if (calls == 2) return _reply([]);
        return _reply(['one']);
      }),
    );
    await _open(tester);
    expect(find.text('当前账号不支持查询模型列表'), findsOneWidget);
    expect(_key('search'), findsNothing);
    expect(_option('ignored'), findsNothing);
    await tester.tap(_key('refresh'));
    await tester.pumpAndSettle();
    expect(find.text('上游未返回可选模型'), findsOneWidget);
    await tester.tap(_key('refresh'));
    await tester.pumpAndSettle();
    await tester.enterText(_key('search'), 'absent');
    await tester.pump();
    expect(find.text('没有匹配的模型'), findsOneWidget);
    expect(find.text('找到 0 / 1 个模型'), findsOneWidget);
    await tester.sendKeyEvent(LogicalKeyboardKey.enter);
    await tester.pump();
    expect(harness.currentState!.selected, isEmpty);
    expect(harness.currentState!.controller.text, 'manual-model');
    await tester.tap(_key('clear'));
    await tester.pump();
    expect(_option('one'), findsOneWidget);
  });

  testWidgets('outside click and Esc close, inside search does not', (
    tester,
  ) async {
    final harness = await _pump(
      tester,
      _client((_) async => _reply(['one', 'two'])),
    );
    await _open(tester);
    await tester.tap(_key('search'));
    await tester.pump();
    expect(_key('panel'), findsOneWidget);
    await tester.tapAt(const Offset(5, 500));
    await tester.pumpAndSettle();
    expect(_key('panel'), findsNothing);
    await _open(tester);
    await tester.sendKeyEvent(LogicalKeyboardKey.escape);
    await tester.pumpAndSettle();
    expect(_key('panel'), findsNothing);
    final fetch = tester.widget<OutlinedButton>(_key('fetch'));
    expect(fetch.focusNode!.hasFocus, isTrue);
    await _open(tester);
    await tester.enterText(
      find.byKey(const ValueKey('manual-input')),
      'hand-id',
    );
    await tester.pump();
    expect(_key('panel'), findsOneWidget);
    await tester.sendKeyEvent(LogicalKeyboardKey.escape);
    await tester.pumpAndSettle();
    expect(_key('panel'), findsNothing);
    expect(harness.currentState!.controller.text, 'hand-id');
    expect(harness.currentState!.selected, isEmpty);
  });

  testWidgets('Esc also closes loading and late response cannot reopen', (
    tester,
  ) async {
    final pending = Completer<http.Response>();
    await _pump(tester, _client((_) => pending.future));
    await tester.tap(_key('fetch'));
    await tester.pump();
    await tester.pump();
    await tester.sendKeyEvent(LogicalKeyboardKey.escape);
    await tester.pump();
    expect(_key('panel'), findsNothing);
    pending.complete(_reply(['late']));
    await tester.pumpAndSettle();
    expect(_key('panel'), findsNothing);
  });

  testWidgets(
    'disabled or missing account blocks only fetch, preserves child',
    (tester) async {
      var calls = 0;
      final harness = await _pump(
        tester,
        _client((_) async {
          ++calls;
          return _reply(['one']);
        }),
      );
      harness.currentState!.update(newEnabled: false);
      await tester.pump();
      expect(tester.widget<OutlinedButton>(_key('fetch')).onPressed, isNull);
      await tester.enterText(
        find.byKey(const ValueKey('manual-input')),
        'custom',
      );
      await tester.pump();
      expect(harness.currentState!.controller.text, 'custom');
      harness.currentState!.update(newEnabled: true, newAccount: '');
      await tester.pump();
      expect(tester.widget<OutlinedButton>(_key('fetch')).onPressed, isNull);
      expect(calls, 0);
      harness.currentState!.update(newAccount: 'account-a');
      await tester.pump();
      await _open(tester);
      harness.currentState!.update(newEnabled: false);
      await tester.pump();
      expect(_key('panel'), findsNothing);
    },
  );

  testWidgets('dark panel uses tokens and truncates long IDs with tooltip', (
    tester,
  ) async {
    final longId = List.filled(20, 'very-long-model-name').join('-');
    await _pump(
      tester,
      _client((_) async => _reply([longId], names: {longId: 'Friendly'})),
      dark: true,
    );
    await _open(tester);
    expect(
      tester.widget<Material>(_key('panel')).color,
      AppTokens.dark.surface,
    );
    final idText = tester.widget<Text>(find.text(longId));
    expect(idText.maxLines, 1);
    expect(idText.overflow, TextOverflow.ellipsis);
    expect(
      find.byWidgetPredicate(
        (widget) => widget is Tooltip && widget.message == '$longId\nFriendly',
      ),
      findsOneWidget,
    );
    expect(tester.takeException(), isNull);
  });

  testWidgets(
    'bottom anchor flips upward in narrow dark viewport without overflow',
    (tester) async {
      await _pump(
        tester,
        _client((_) async => _reply(List.generate(100, (i) => 'model-$i'))),
        dark: true,
        alignment: Alignment.bottomCenter,
        size: const Size(320, 520),
      );
      final before = tester.getRect(
        find.byKey(const ValueKey('unchanged-card')),
      );
      await _open(tester);
      final panel = tester.getRect(_key('panel'));
      final fetch = tester.getRect(_key('fetch'));
      expect(panel.bottom, lessThan(fetch.top));
      expect(panel.top, greaterThanOrEqualTo(8));
      expect(panel.left, greaterThanOrEqualTo(8));
      expect(panel.right, lessThanOrEqualTo(312));
      expect(panel.height, lessThanOrEqualTo(420));
      expect(panel.width, lessThanOrEqualTo(288));
      expect(
        tester.getRect(find.byKey(const ValueKey('unchanged-card'))),
        before,
      );
      await tester.enterText(_key('search'), 'no-result');
      await tester.pump();
      expect(find.text('没有匹配的模型'), findsOneWidget);
      expect(tester.takeException(), isNull);
      await tester.sendKeyEvent(LogicalKeyboardKey.escape);
      await tester.pump();
      expect(_key('panel'), findsNothing);
    },
  );

  testWidgets(
    'short viewport keeps status and retry scrollable without overflow',
    (tester) async {
      await _pump(
        tester,
        _client((_) async => http.Response('failure', 500)),
        size: const Size(320, 240),
      );
      await _open(tester);
      final panel = tester.getRect(_key('panel'));
      expect(panel.top, greaterThanOrEqualTo(8));
      expect(panel.bottom, lessThanOrEqualTo(232));
      expect(tester.takeException(), isNull);
      await tester.tap(_key('close'));
      await tester.pumpAndSettle();
      expect(_key('panel'), findsNothing);
    },
  );
}

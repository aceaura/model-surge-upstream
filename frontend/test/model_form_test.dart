import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:msu_admin/api_client.dart';
import 'package:msu_admin/models.dart';
import 'package:msu_admin/pages/model_form.dart';
import 'package:msu_admin/theme.dart';
import 'package:msu_admin/ui/styled_dropdown.dart';

final providers = [
  ProviderSpec.fromJson(const {
    'id': 'kimi.global.subscribe.coding',
    'display_name': 'Moonshot Kimi',
    'website': 'https://platform.moonshot.cn',
    'base_url': 'https://api.kimi.com/coding',
    'protocols': ['anthropic', 'chat_completions'],
    'auth': 'anthropic_key',
    'credential': 'api_key',
  }),
  ProviderSpec.fromJson(const {
    'id': 'openai.global.api.standard',
    'display_name': 'OpenAI',
    'website': 'https://openai.com',
    'base_url': 'https://api.openai.com',
    'protocols': ['chat_completions', 'responses'],
    'auth': 'bearer',
    'credential': 'api_key',
  }),
];

final accounts = [
  Account.fromJson(const {
    'name': 'kimi-1',
    'provider_id': 'kimi.global.subscribe.coding',
    'credential': {'kind': 'api_key', 'api_key': 'sk-l***efgh'},
    'base_url': '',
    'headers': <String, dynamic>{},
    'enabled': true,
  }),
  Account.fromJson(const {
    'name': 'oa-1',
    'provider_id': 'openai.global.api.standard',
    'credential': {'kind': 'api_key', 'api_key': 'sk-o***wxyz'},
    'base_url': '',
    'headers': <String, dynamic>{},
    'enabled': true,
  }),
];

ApiClient stubClient() => ApiClient(
  baseUrl: 'http://127.0.0.1:8080',
  adminKey: 'adm',
  httpClient: MockClient((_) async => http.Response('{}', 200)),
);

/// 表单已是整页路由组件,直接作为 home pump。
Future<void> pumpForm(
  WidgetTester tester, {
  UpstreamModel? editing,
  UpstreamModel? copyFrom,
  ApiClient? client,
}) async {
  tester.view.physicalSize = const Size(1200, 900);
  tester.view.devicePixelRatio = 1.0;
  addTearDown(tester.view.reset);
  await tester.pumpWidget(
    MaterialApp(
      theme: buildAppTheme(),
      home: ModelForm(
        client: client ?? stubClient(),
        accounts: accounts,
        providers: providers,
        initialAccount: 'kimi-1',
        onDone: (_) {},
        editing: editing,
        copyFrom: copyFrom,
      ),
    ),
  );
  await tester.pumpAndSettle();
}

/// 指定字段分区内的自绘下拉触发器(label 在框外,按分区 key 找)。
Finder dropdownIn(String fieldKey) => find.descendant(
  of: find.byKey(ValueKey(fieldKey)),
  matching: find.byType(StyledDropdown),
);

Finder jsonBox(String fieldKey) => find.descendant(
  of: find.byKey(ValueKey(fieldKey)),
  matching: find.byType(TextField),
);

void main() {
  for (final mode in ['create', 'edit', 'copy']) {
    testWidgets(
      '$mode selects upstream ID and submits only the intended field',
      (tester) async {
        Map<String, dynamic>? submitted;
        String? method;
        final source = UpstreamModel.fromJson(const {
          'id': 'kimi-1/existing',
          'account': 'kimi-1',
          'native_model': 'old-model',
          'protocol': 'anthropic',
          'context_window': 128000,
          'defaults': {'temperature': 0.6},
          'overrides': {'max_tokens': 8192},
          'compact': {'mode': 'passive'},
          'efforts': [
            {'name': '1', 'value': 'high'},
          ],
          'effort_format': 'chat_completions',
          'enabled': false,
        });
        final client = ApiClient(
          baseUrl: 'http://127.0.0.1:8080',
          adminKey: 'adm',
          httpClient: MockClient((request) async {
            if (request.url.path.endsWith('/upstream-models')) {
              expect(
                request.url.path,
                '/admin/accounts/kimi-1/upstream-models',
              );
              return http.Response(
                jsonEncode({
                  'queryable': true,
                  'models': [
                    {'id': 'qwen3.8-max', 'display_name': '千问 Max'},
                  ],
                }),
                200,
                headers: {'content-type': 'application/json'},
              );
            }
            method = request.method;
            submitted =
                jsonDecode(utf8.decode(request.bodyBytes))
                    as Map<String, dynamic>;
            return http.Response(
              jsonEncode({
                'model': {'id': source.id, ...submitted!},
              }),
              200,
              headers: {'content-type': 'application/json'},
            );
          }),
        );
        await pumpForm(
          tester,
          client: client,
          editing: mode == 'edit' ? source : null,
          copyFrom: mode == 'copy' ? source : null,
        );
        if (mode == 'create') {
          await tester.enterText(
            find.byKey(const ValueKey('model-id')),
            'kimi-1/new',
          );
        }
        final oldContextPosition = tester.getTopLeft(find.text('上下文限制'));
        await tester.tap(find.byKey(const ValueKey('upstream-model-fetch')));
        await tester.pumpAndSettle();
        expect(tester.getTopLeft(find.text('上下文限制')), oldContextPosition);
        await tester.enterText(
          find.byKey(const ValueKey('upstream-model-search')),
          '千问',
        );
        await tester.pumpAndSettle();
        await tester.tap(
          find.byKey(const ValueKey('upstream-model-option-qwen3.8-max')),
        );
        await tester.pumpAndSettle();
        expect(
          find.byKey(const ValueKey('upstream-model-panel')),
          findsNothing,
        );
        expect(
          tester
              .widget<TextFormField>(find.byKey(const ValueKey('model-native')))
              .controller!
              .text,
          'qwen3.8-max',
        );
        if (mode == 'copy') {
          expect(
            tester
                .widget<TextFormField>(find.byKey(const ValueKey('model-id')))
                .controller!
                .text,
            'kimi-1/existing-copy',
          );
        }
        await tester.tap(
          find.widgetWithText(FilledButton, mode == 'edit' ? '保存' : '创建'),
        );
        await tester.pumpAndSettle();
        expect(method, mode == 'edit' ? 'PUT' : 'POST');
        if (mode == 'edit') {
          // 编辑恒带标识:未改时即原值,改了即改名目标。
          expect(submitted!['id'], 'kimi-1/existing');
        }
        expect(submitted!['native_model'], 'qwen3.8-max');
        expect(submitted!['account'], 'kimi-1');
        expect(submitted!['protocol'], 'anthropic');
        if (mode != 'create') {
          expect(submitted!['context_window'], 128000);
          expect(submitted!['defaults'], {'temperature': 0.6});
          expect(submitted!['overrides'], {'max_tokens': 8192});
          expect(submitted!['efforts'], [
            {'name': '1', 'value': 'high'},
          ]);
          expect(submitted!['effort_format'], 'openai_chat');
          expect(submitted!['enabled'], isFalse);
        }
        expect(tester.takeException(), isNull);
      },
    );
  }

  testWidgets('upstream list follows the account selected in the form', (
    tester,
  ) async {
    final queried = <String>[];
    final client = ApiClient(
      baseUrl: 'http://127.0.0.1:8080',
      adminKey: 'adm',
      httpClient: MockClient((request) async {
        queried.add(request.url.path);
        return http.Response(
          jsonEncode({
            'queryable': true,
            'models': [
              {'id': 'gpt-account-model'},
            ],
          }),
          200,
          headers: {'content-type': 'application/json'},
        );
      }),
    );
    await pumpForm(tester, client: client);
    await tester.enterText(
      find.byKey(const ValueKey('model-native')),
      'manual-model',
    );
    await tester.tap(dropdownIn('model-account-field'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('oa-1').last);
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const ValueKey('upstream-model-fetch')));
    await tester.pumpAndSettle();
    expect(queried, ['/admin/accounts/oa-1/upstream-models']);
    expect(
      tester
          .widget<TextFormField>(find.byKey(const ValueKey('model-native')))
          .controller!
          .text,
      'manual-model',
    );
    await tester.tap(
      find.byKey(const ValueKey('upstream-model-option-gpt-account-model')),
    );
    await tester.pumpAndSettle();
    expect(
      tester
          .widget<TextFormField>(find.byKey(const ValueKey('model-native')))
          .controller!
          .text,
      'gpt-account-model',
    );
    expect(
      find.text('oa-1 · chat_completions · gpt-account-model'),
      findsOneWidget,
    );
  });

  testWidgets('protocol options come from the selected account provider', (
    tester,
  ) async {
    await pumpForm(tester);

    await tester.tap(dropdownIn('model-protocol-field'));
    await tester.pumpAndSettle();
    // kimi 支持 anthropic 与 chat_completions，不支持 responses。
    expect(find.text('anthropic'), findsWidgets);
    expect(find.text('responses'), findsNothing);

    await tester.tap(find.text('anthropic').last);
    await tester.pumpAndSettle();
  });

  testWidgets('switching account refreshes the protocol options', (
    tester,
  ) async {
    await pumpForm(tester);

    await tester.tap(dropdownIn('model-account-field'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('oa-1').last);
    await tester.pumpAndSettle();

    await tester.tap(dropdownIn('model-protocol-field'));
    await tester.pumpAndSettle();
    // openai 支持 responses 但不支持 anthropic。
    expect(find.text('responses'), findsWidgets);
    expect(find.text('anthropic'), findsNothing);
  });

  testWidgets('invalid params json disables the submit button', (tester) async {
    await pumpForm(tester);

    final submit = find.widgetWithText(FilledButton, '创建');
    expect(tester.widget<FilledButton>(submit).onPressed, isNotNull);

    await tester.enterText(jsonBox('model-defaults'), '{bad');
    await tester.pump();

    expect(find.textContaining('JSON 格式错误'), findsOneWidget);
    expect(
      tester.widget<FilledButton>(submit).onPressed,
      isNull,
      reason: 'invalid json must block submission',
    );

    await tester.enterText(jsonBox('model-defaults'), '{"temperature":0.6}');
    await tester.pump();
    expect(tester.widget<FilledButton>(submit).onPressed, isNotNull);
  });

  testWidgets('editing prefills the existing values', (tester) async {
    final editing = UpstreamModel.fromJson(const {
      'id': 'kimi-1/k2',
      'account': 'kimi-1',
      'native_model': 'kimi-k2-turbo',
      'protocol': 'anthropic',
      'context_window': 262144,
      'defaults': {'temperature': 0.6},
      'overrides': {'max_tokens': 8192},
      'enabled': true,
    });
    await pumpForm(tester, editing: editing);

    expect(find.text('kimi-k2-turbo'), findsAtLeastNWidgets(1));
    // 上下文窗口按 k 单位回显(262144 tokens = 262.144k)
    expect(find.text('262.144'), findsAtLeastNWidgets(1));
    expect(find.textContaining('"temperature": 0.6'), findsAtLeastNWidgets(1));
    expect(find.textContaining('"max_tokens": 8192'), findsAtLeastNWidgets(1));
    // 编辑态标识字段可改(改名保存),预填当前标识。
    final idField = tester.widget<TextFormField>(
      find.byKey(const ValueKey('model-id')),
    );
    expect(idField.controller!.text, 'kimi-1/k2');
  });

  testWidgets('copy prefills config but keeps create semantics', (
    tester,
  ) async {
    final source = UpstreamModel.fromJson(const {
      'id': 'kimi-1/k2',
      'account': 'kimi-1',
      'native_model': 'kimi-k2-turbo',
      'protocol': 'anthropic',
      'context_window': 262144,
      'defaults': {'temperature': 0.6},
      'overrides': {'max_tokens': 8192},
      'enabled': true,
    });
    await pumpForm(tester, copyFrom: source);

    expect(find.text('拷贝 kimi-1/k2'), findsOneWidget);
    // 标识加 -copy 后缀,且标识字段可编辑(新建语义)
    final idField = tester.widget<TextFormField>(
      find.byKey(const ValueKey('model-id')),
    );
    expect(idField.controller!.text, 'kimi-1/k2-copy');
    expect(find.text('kimi-k2-turbo'), findsAtLeastNWidgets(1));
    expect(
      find.text('262.144'),
      findsAtLeastNWidgets(1),
      reason: '上下文窗口随源模型预填',
    );
    expect(find.textContaining('"temperature": 0.6'), findsAtLeastNWidgets(1));
    expect(find.textContaining('"max_tokens": 8192'), findsAtLeastNWidgets(1));
    expect(
      find.text('创建'),
      findsOneWidget,
      reason: 'copy is a create, not an edit',
    );
  });

  testWidgets('form groups fields into three collapsible sections', (
    tester,
  ) async {
    final editing = UpstreamModel.fromJson(const {
      'id': 'kimi-1/k2',
      'account': 'kimi-1',
      'native_model': 'kimi-k2-turbo',
      'protocol': 'anthropic',
      'context_window': 262144,
      'defaults': {'temperature': 0.6},
      'overrides': <String, dynamic>{},
      'enabled': true,
    });
    await pumpForm(tester, editing: editing);

    expect(find.text('基本信息'), findsOneWidget);
    expect(find.text('上下文限制'), findsOneWidget);
    expect(find.text('附加参数'), findsOneWidget);
    expect(
      find.text('kimi-1 · anthropic · kimi-k2-turbo'),
      findsOneWidget,
      reason: '收起也能靠副标题辨认基本信息',
    );
    expect(find.text('窗口 262.144k · 元数据'), findsOneWidget);
    expect(
      find.textContaining('"temperature": 0.6'),
      findsAtLeastNWidgets(1),
      reason: '已配参数时附加参数分栏默认展开',
    );

    // 折叠再展开,已填内容不丢(分栏只裁剪不卸载)。
    await tester.tap(find.text('基本信息'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('基本信息'));
    await tester.pumpAndSettle();
    expect(find.text('kimi-k2-turbo'), findsAtLeastNWidgets(1));
  });

  testWidgets('params section expands on demand for empty config', (
    tester,
  ) async {
    await pumpForm(tester);

    expect(find.text('附加参数'), findsOneWidget);
    await tester.tap(find.text('附加参数'));
    await tester.pumpAndSettle();
    expect(find.byKey(const ValueKey('model-defaults')), findsOneWidget);
    expect(find.byKey(const ValueKey('model-overrides')), findsOneWidget);
  });

  testWidgets('context window rejects non-numeric input', (tester) async {
    await pumpForm(tester);
    await tester.enterText(find.byKey(const ValueKey('model-context')), 'abc');
    await tester.tap(find.widgetWithText(FilledButton, '创建'));
    await tester.pump();
    expect(find.text('请填写数字'), findsOneWidget);
  });

  testWidgets('context window rejects negative input', (tester) async {
    await pumpForm(tester);
    await tester.enterText(find.byKey(const ValueKey('model-context')), '-5');
    await tester.tap(find.widgetWithText(FilledButton, '创建'));
    await tester.pump();
    expect(find.text('不能为负数'), findsOneWidget);
  });

  testWidgets('context window is submitted in tokens from k input', (
    tester,
  ) async {
    Map<String, dynamic>? sentBody;
    final client = ApiClient(
      baseUrl: 'http://127.0.0.1:8080',
      adminKey: 'adm',
      httpClient: MockClient((req) async {
        sentBody =
            jsonDecode(utf8.decode(req.bodyBytes)) as Map<String, dynamic>;
        return http.Response('{}', 200);
      }),
    );
    await pumpForm(tester, client: client);

    await tester.enterText(find.byKey(const ValueKey('model-id')), 'kimi-1/k2');
    await tester.enterText(
      find.byKey(const ValueKey('model-native')),
      'kimi-k2-turbo',
    );
    // 256k 应以 256000 tokens 提交
    await tester.enterText(find.byKey(const ValueKey('model-context')), '256');
    await tester.tap(find.widgetWithText(FilledButton, '创建'));
    await tester.pumpAndSettle();

    expect(sentBody, isNotNull);
    expect(sentBody!['context_window'], 256000);

    // 冲刷提示框的 2.4s 驻留定时器与滑出动画,避免遗留 Timer。
    await tester.pump(const Duration(seconds: 3));
    await tester.pumpAndSettle();
  });

  testWidgets('压缩策略:存量 auto 归一拦截,下拉只留元数据/拦截', (tester) async {
    final source = UpstreamModel.fromJson(const {
      'id': 'kimi-1/legacy',
      'account': 'kimi-1',
      'native_model': 'k3-256k',
      'protocol': 'chat_completions',
      'context_window': 200000,
      'compact': {'mode': 'auto', 'keep_turns': 3},
      'enabled': true,
    });
    await pumpForm(tester, editing: source);

    final dropdown = tester.widget<StyledDropdown>(
      dropdownIn('model-compact-mode-field'),
    );
    expect(dropdown.value, 'error');
    expect(dropdown.options, const ['passive', 'error']);
    // 保留轮数随 auto 一并消失;拦截模式下阈值行在场。
    expect(find.byKey(const ValueKey('model-compact-keep')), findsNothing);
    expect(
      find.byKey(const ValueKey('model-compact-threshold')),
      findsOneWidget,
    );
  });

  testWidgets('推理档:新建默认开关开且无档位行,数字映射值行动态增删随提交上行', (tester) async {
    Map<String, dynamic>? sentBody;
    final client = ApiClient(
      baseUrl: 'http://127.0.0.1:8080',
      adminKey: 'adm',
      httpClient: MockClient((req) async {
        sentBody =
            jsonDecode(utf8.decode(req.bodyBytes)) as Map<String, dynamic>;
        return http.Response('{}', 200);
      }),
    );
    await pumpForm(tester, client: client);

    // 没有自动模式:分区默认展开;关闭思考常驻开关(0 档)默认开,档位行初始为空。
    expect(find.text('推理档'), findsOneWidget);
    expect(find.textContaining('关闭思考'), findsAtLeastNWidgets(1));
    expect(find.text('自动（跟随上游声明）'), findsNothing);
    expect(
      tester
          .widget<Switch>(find.byKey(const ValueKey('model-effort-off')))
          .value,
      isTrue,
    );
    expect(find.byKey(const ValueKey('model-effort-value-0')), findsNothing);
    expect(
      find.byKey(const ValueKey('model-effort-name-0')),
      findsNothing,
      reason: '名输入已废除,档位=行号',
    );

    // 加一行填 ultra(行号 1 即 1 档);再加一行留空(空值行提交时丢弃)。
    await tester.ensureVisible(find.byKey(const ValueKey('model-effort-add')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const ValueKey('model-effort-add')));
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byKey(const ValueKey('model-effort-value-0')),
      'ultra',
    );
    await tester.ensureVisible(find.byKey(const ValueKey('model-effort-add')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const ValueKey('model-effort-add')));
    await tester.pumpAndSettle();

    // 上游格式下拉框在双端声明行:默认协议内置,改选 openai_chat
    // (顶层 reasoning_effort)随提交上行。
    await tester.ensureVisible(
      find.byKey(const ValueKey('model-effort-format')),
    );
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const ValueKey('model-effort-format')));
    await tester.pumpAndSettle();
    await tester.tap(
      find.text('openai_chat · 顶层 reasoning_effort'),
    );
    await tester.pumpAndSettle();

    await tester.ensureVisible(find.byKey(const ValueKey('model-id')));
    await tester.pumpAndSettle();
    await tester.enterText(find.byKey(const ValueKey('model-id')), 'kimi-1/k2');
    await tester.enterText(
      find.byKey(const ValueKey('model-native')),
      'kimi-k2-turbo',
    );
    await tester.tap(find.widgetWithText(FilledButton, '创建'));
    await tester.pumpAndSettle();

    expect(sentBody, isNotNull);
    expect(sentBody!['efforts'], [
      {'name': '0', 'value': 'none'},
      {'name': '1', 'value': 'ultra'},
    ]);
    expect(sentBody!['effort_format'], 'openai_chat');

    await tester.pump(const Duration(seconds: 3));
    await tester.pumpAndSettle();
  });

  testWidgets('推理档:编辑显式条目(无 none)开关关,删行与拨开关随提交', (tester) async {
    Map<String, dynamic>? sentBody;
    final client = ApiClient(
      baseUrl: 'http://127.0.0.1:8080',
      adminKey: 'adm',
      httpClient: MockClient((req) async {
        sentBody =
            jsonDecode(utf8.decode(req.bodyBytes)) as Map<String, dynamic>;
        return http.Response('{}', 200);
      }),
    );
    final editing = UpstreamModel.fromJson(const {
      'id': 'kimi-1/k2',
      'account': 'kimi-1',
      'native_model': 'kimi-k2-turbo',
      'protocol': 'anthropic',
      'context_window': 0,
      'defaults': <String, dynamic>{},
      'overrides': <String, dynamic>{},
      'efforts': [
        {'name': '低', 'value': 'low'},
        {'name': '高', 'value': 'high'},
      ],
      'efforts_effective': [
        {'name': '低', 'value': 'low'},
        {'name': '高', 'value': 'high'},
      ],
      'enabled': true,
    });
    await pumpForm(tester, editing: editing, client: client);

    // 显式列表无 none:开关关;旧中文名废弃,按行号重排为 1·low / 2·high。
    expect(
      tester
          .widget<Switch>(find.byKey(const ValueKey('model-effort-off')))
          .value,
      isFalse,
    );
    expect(find.text('1·low / 2·high'), findsOneWidget);
    expect(
      tester
          .widget<TextFormField>(
            find.byKey(const ValueKey('model-effort-value-0')),
          )
          .controller!
          .text,
      'low',
    );
    expect(
      tester
          .widget<TextFormField>(
            find.byKey(const ValueKey('model-effort-value-1')),
          )
          .controller!
          .text,
      'high',
    );

    // 删掉首行(low)并拨开关闭思考再保存:0 档在最前,high 重排为 1 档。
    await tester.ensureVisible(
      find.byKey(const ValueKey('model-effort-del-0')),
    );
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const ValueKey('model-effort-del-0')));
    await tester.pumpAndSettle();
    await tester.ensureVisible(find.byKey(const ValueKey('model-effort-off')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const ValueKey('model-effort-off')));
    await tester.pumpAndSettle();
    await tester.tap(find.widgetWithText(FilledButton, '保存'));
    await tester.pumpAndSettle();
    expect(sentBody, isNotNull);
    expect(sentBody!['efforts'], [
      {'name': '0', 'value': 'none'},
      {'name': '1', 'value': 'high'},
    ]);

    await tester.pump(const Duration(seconds: 3));
    await tester.pumpAndSettle();
  });

  testWidgets('推理档:存量自动(null)模型编辑时开关开+有效列表预填,保存落为显式', (tester) async {
    Map<String, dynamic>? sentBody;
    final client = ApiClient(
      baseUrl: 'http://127.0.0.1:8080',
      adminKey: 'adm',
      httpClient: MockClient((req) async {
        sentBody =
            jsonDecode(utf8.decode(req.bodyBytes)) as Map<String, dynamic>;
        return http.Response('{}', 200);
      }),
    );
    final editing = UpstreamModel.fromJson(const {
      'id': 'kimi-1/k2',
      'account': 'kimi-1',
      'native_model': 'kimi-k2-turbo',
      'protocol': 'anthropic',
      'context_window': 0,
      'defaults': <String, dynamic>{},
      'overrides': <String, dynamic>{},
      'efforts': null,
      'efforts_effective': [
        {'name': '低', 'value': 'low'},
        {'name': 'ultra', 'value': 'ultra'},
      ],
      'enabled': true,
    });
    await pumpForm(tester, editing: editing, client: client);

    // 开关默认开,行=有效列表两条值(无 none 行),副标题按数字档展示。
    expect(
      tester
          .widget<Switch>(find.byKey(const ValueKey('model-effort-off')))
          .value,
      isTrue,
    );
    expect(find.text('0·关闭思考 / 1·low / 2·ultra'), findsOneWidget);
    expect(find.byKey(const ValueKey('model-effort-value-1')), findsOneWidget);
    expect(find.byKey(const ValueKey('model-effort-value-2')), findsNothing);
    expect(
      tester
          .widget<TextFormField>(
            find.byKey(const ValueKey('model-effort-value-0')),
          )
          .controller!
          .text,
      'low',
    );

    await tester.tap(find.widgetWithText(FilledButton, '保存'));
    await tester.pumpAndSettle();
    expect(sentBody, isNotNull);
    expect(sentBody!['efforts'], [
      {'name': '0', 'value': 'none'},
      {'name': '1', 'value': 'low'},
      {'name': '2', 'value': 'ultra'},
    ]);

    await tester.pump(const Duration(seconds: 3));
    await tester.pumpAndSettle();
  });

  testWidgets('推理档:双端声明(入口 effort_index → 上游 anthropic_budget)+关思考落定+预算覆盖随提交', (tester) async {
    Map<String, dynamic>? sentBody;
    final client = ApiClient(
      baseUrl: 'http://127.0.0.1:8080',
      adminKey: 'adm',
      httpClient: MockClient((req) async {
        sentBody =
            jsonDecode(utf8.decode(req.bodyBytes)) as Map<String, dynamic>;
        return http.Response('{}', 200);
      }),
    );
    await pumpForm(tester, client: client);

    // 未选定配对(上游=auto):即便协议是 anthropic,关思考落定与预算列都不出现
    // ——配对专属字段只在显式选定上游格式后动态出现。
    expect(find.byKey(const ValueKey('model-effort-off-policy')), findsNothing);
    expect(find.byKey(const ValueKey('model-effort-budget-0')), findsNothing);

    // 入口格式 → effort_index。
    await tester.ensureVisible(find.byKey(const ValueKey('model-effort-in')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const ValueKey('model-effort-in')));
    await tester.pumpAndSettle();
    await tester.tap(find.text('effort_index · 顶层 reasoning_level 数字档'));
    await tester.pumpAndSettle();

    // 上游格式 → anthropic_budget(选定配对):预算列与关思考落定随即动态出现。
    await tester.ensureVisible(
      find.byKey(const ValueKey('model-effort-format')),
    );
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const ValueKey('model-effort-format')));
    await tester.pumpAndSettle();
    await tester.tap(find.text('anthropic_budget · thinking enabled + budget_tokens'));
    await tester.pumpAndSettle();
    expect(find.byKey(const ValueKey('model-effort-off-policy')), findsOneWidget);

    // 关思考落定 → between_tools。
    await tester.ensureVisible(
      find.byKey(const ValueKey('model-effort-off-policy')),
    );
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const ValueKey('model-effort-off-policy')));
    await tester.pumpAndSettle();
    await tester.tap(find.text('between_tools（Sonnet 5.5 顶替 disabled）'));
    await tester.pumpAndSettle();

    // 加一档 high,预算覆盖 12000。
    await tester.ensureVisible(find.byKey(const ValueKey('model-effort-add')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const ValueKey('model-effort-add')));
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byKey(const ValueKey('model-effort-value-0')),
      'high',
    );
    await tester.enterText(
      find.byKey(const ValueKey('model-effort-budget-0')),
      '12000',
    );

    await tester.ensureVisible(find.byKey(const ValueKey('model-id')));
    await tester.pumpAndSettle();
    await tester.enterText(find.byKey(const ValueKey('model-id')), 'kimi-1/k2');
    await tester.enterText(
      find.byKey(const ValueKey('model-native')),
      'kimi-k2-turbo',
    );
    await tester.tap(find.widgetWithText(FilledButton, '创建'));
    await tester.pumpAndSettle();

    expect(sentBody, isNotNull);
    expect(sentBody!['effort_in'], 'effort_index');
    expect(sentBody!['effort_format'], 'anthropic_budget');
    expect(sentBody!['effort_off'], 'between_tools');
    expect(sentBody!['effort_budgets'], {'high': 12000});
    expect(sentBody!['efforts'], [
      {'name': '0', 'value': 'none'},
      {'name': '1', 'value': 'high'},
    ]);

    await tester.pump(const Duration(seconds: 3));
    await tester.pumpAndSettle();
  });

  testWidgets('推理档:存量双端字段编辑时回填(入口/关思考/预算)', (tester) async {
    final editing = UpstreamModel.fromJson(const {
      'id': 'kimi-1/k2',
      'account': 'kimi-1',
      'native_model': 'kimi-k2-turbo',
      'protocol': 'anthropic',
      'context_window': 0,
      'defaults': <String, dynamic>{},
      'overrides': <String, dynamic>{},
      'efforts': [
        {'name': '0', 'value': 'none'},
        {'name': '1', 'value': 'high'},
      ],
      'effort_format': 'anthropic_budget',
      'effort_in': 'openai_chat',
      'effort_off': 'omit',
      'effort_budgets': {'high': 16000},
      'enabled': true,
    });
    await pumpForm(tester, editing: editing, client: stubClient());

    // 入口/上游/关思考下拉回填选中值;预算列出现且预填 16000。
    expect(
      tester.widget<StyledDropdown>(dropdownIn('model-effort-in-field')).value,
      'openai_chat',
    );
    expect(
      tester
          .widget<StyledDropdown>(dropdownIn('model-effort-format-field'))
          .value,
      'anthropic_budget',
    );
    expect(
      tester
          .widget<StyledDropdown>(dropdownIn('model-effort-off-field'))
          .value,
      'omit',
    );
    expect(
      tester
          .widget<TextFormField>(
            find.byKey(const ValueKey('model-effort-budget-0')),
          )
          .controller!
          .text,
      '16000',
    );
  });
}

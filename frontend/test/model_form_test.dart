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
  String initialAccount = 'kimi-1',
  List<Account>? accountsOverride,
  List<ProviderSpec>? providersOverride,
}) async {
  tester.view.physicalSize = const Size(1200, 900);
  tester.view.devicePixelRatio = 1.0;
  addTearDown(tester.view.reset);
  await tester.pumpWidget(
    MaterialApp(
      theme: buildAppTheme(),
      home: ModelForm(
        client: client ?? stubClient(),
        accounts: accountsOverride ?? accounts,
        providers: providersOverride ?? providers,
        initialAccount: initialAccount,
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
          'defaults': {'temperature': 0.6},
          'overrides': {'max_tokens': 8192},
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
        final oldSectionPosition = tester.getTopLeft(find.text('推理档'));
        await tester.tap(find.byKey(const ValueKey('upstream-model-fetch')));
        await tester.pumpAndSettle();
        expect(tester.getTopLeft(find.text('推理档')), oldSectionPosition);
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
        expect(submitted!.containsKey('context_window'), isFalse);
        expect(submitted!.containsKey('compact'), isFalse);
        if (mode != 'create') {
          expect(submitted!['defaults'], {'temperature': 0.6});
          expect(submitted!['overrides'], {'max_tokens': 8192});
          expect(submitted!['efforts'], [
            // 存量「行号即档号」读时迁移为映射对(与后端同口径),保存即落新形态。
            {'name': 'high', 'value': 'high'},
          ]);
          // 存量 chat_completions 归一为 openai_chat 后保留(同族约束已撤,
          // 跨族值不再回落族首项)。
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
      'defaults': {'temperature': 0.6},
      'overrides': {'max_tokens': 8192},
      'enabled': true,
    });
    await pumpForm(tester, editing: editing);

    expect(find.text('kimi-k2-turbo'), findsAtLeastNWidgets(1));
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
    expect(find.textContaining('"temperature": 0.6'), findsAtLeastNWidgets(1));
    expect(find.textContaining('"max_tokens": 8192'), findsAtLeastNWidgets(1));
    expect(
      find.text('创建'),
      findsOneWidget,
      reason: 'copy is a create, not an edit',
    );
  });

  testWidgets('form groups fields into collapsible sections', (tester) async {
    final editing = UpstreamModel.fromJson(const {
      'id': 'kimi-1/k2',
      'account': 'kimi-1',
      'native_model': 'kimi-k2-turbo',
      'protocol': 'anthropic',
      'defaults': {'temperature': 0.6},
      'overrides': <String, dynamic>{},
      'enabled': true,
    });
    await pumpForm(tester, editing: editing);

    expect(find.text('基本信息'), findsOneWidget);
    expect(find.text('推理档'), findsOneWidget);
    expect(find.text('附加参数'), findsOneWidget);
    expect(find.text('上下文限制'), findsNothing);
    expect(find.byKey(const ValueKey('model-context')), findsNothing);
    expect(find.byKey(const ValueKey('model-compact-switch')), findsNothing);
    expect(
      find.text('kimi-1 · anthropic · kimi-k2-turbo'),
      findsOneWidget,
      reason: '收起也能靠副标题辨认基本信息',
    );
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

  testWidgets('推理档:新建默认无映射行,行式映射对动态增删随提交上行', (tester) async {
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

    // 行式映射对(2026-10-11):分区默认展开;0 档开关已删(none 进两端词表),
    // 初始无映射行,副标题显「不支持」。
    expect(find.text('推理档'), findsOneWidget);
    expect(find.byKey(const ValueKey('model-effort-off')), findsNothing);
    expect(find.text('自动（跟随上游声明）'), findsNothing);
    expect(find.byKey(const ValueKey('model-effort-name-0')), findsNothing);
    expect(
      find.textContaining('不支持 · 下游 openai_chat → 上游 openai_chat'),
      findsOneWidget,
    );

    // 加两行:默认 low→low;删第二行,首行保留。
    await tester.ensureVisible(find.byKey(const ValueKey('model-effort-add')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const ValueKey('model-effort-add')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const ValueKey('model-effort-add')));
    await tester.pumpAndSettle();
    expect(find.byKey(const ValueKey('model-effort-name-0')), findsOneWidget);
    expect(find.byKey(const ValueKey('model-effort-name-1')), findsOneWidget);
    await tester.tap(find.byKey(const ValueKey('model-effort-del-1')));
    await tester.pumpAndSettle();
    expect(find.byKey(const ValueKey('model-effort-name-1')), findsNothing);

    // 上游格式改选 anthropic_budget:右列变预算编辑框(词表外值矫正为 4096),
    // 改成 12000 随提交。
    await tester.ensureVisible(
      find.byKey(const ValueKey('model-effort-format')),
    );
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const ValueKey('model-effort-format')));
    await tester.pumpAndSettle();
    await tester.tap(find.text('anthropic_budget').last);
    await tester.pumpAndSettle();
    final valueField = find.descendant(
      of: find.byKey(const ValueKey('model-effort-mode-field')),
      matching: find.byType(TextFormField),
    );
    expect(valueField, findsOneWidget);
    await tester.enterText(valueField, '12000');
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
      // 新行左列默认取下游词表第一个非 none 档(openai 词表为 minimal)。
      {'name': 'minimal', 'value': '12000'},
    ]);
    expect(sentBody!['effort_format'], 'anthropic_budget');

    await tester.pump(const Duration(seconds: 3));
    await tester.pumpAndSettle();
  });

  testWidgets('推理档:编辑显式映射对,删行随提交', (tester) async {
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
      'defaults': <String, dynamic>{},
      'overrides': <String, dynamic>{},
      'efforts': [
        {'name': 'low', 'value': 'low'},
        {'name': 'high', 'value': 'xhigh'},
      ],
      'efforts_effective': [
        {'name': 'low', 'value': 'low'},
        {'name': 'high', 'value': 'xhigh'},
      ],
      'enabled': true,
    });
    await pumpForm(tester, editing: editing, client: client);

    // 映射对回填:双下拉选中 low→low / high→xhigh;双端格式缺省落词表首项
    // openai_chat,副标题显「2 行映射」并常驻转换方向。
    expect(
      find.text('2 行映射 · 下游 openai_chat → 上游 openai_chat'),
      findsOneWidget,
    );
    StyledDropdown rowDropdown(String key) => tester.widget<StyledDropdown>(
      find.descendant(
        of: find.byKey(ValueKey(key)),
        matching: find.byType(StyledDropdown),
      ),
    );
    expect(rowDropdown('model-effort-name-0').value, 'low');
    expect(rowDropdown('model-effort-value-0').value, 'low');
    expect(rowDropdown('model-effort-name-1').value, 'high');
    expect(rowDropdown('model-effort-value-1').value, 'xhigh');

    // 删掉首行(low→low)再保存:仅剩 high→xhigh 一对。
    await tester.ensureVisible(
      find.byKey(const ValueKey('model-effort-del-0')),
    );
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const ValueKey('model-effort-del-0')));
    await tester.pumpAndSettle();
    await tester.tap(find.widgetWithText(FilledButton, '保存'));
    await tester.pumpAndSettle();
    expect(sentBody, isNotNull);
    expect(sentBody!['efforts'], [
      {'name': 'high', 'value': 'xhigh'},
    ]);

    await tester.pump(const Duration(seconds: 3));
    await tester.pumpAndSettle();
  });

  testWidgets('推理档:存量自动(null)模型编辑时有效列表预填,保存落为显式', (tester) async {
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
      'defaults': <String, dynamic>{},
      'overrides': <String, dynamic>{},
      'efforts': null,
      'efforts_effective': [
        {'name': 'low', 'value': 'low'},
        {'name': 'ultra', 'value': 'ultra'},
      ],
      'enabled': true,
    });
    await pumpForm(tester, editing: editing, client: client);

    // 行=有效列表两条(自动模式现为恒等映射对,私有档 ultra 原值保留);
    // 副标题显「2 行映射」并常驻缺省双端格式(词表首项 openai_chat)。
    expect(
      find.text('2 行映射 · 下游 openai_chat → 上游 openai_chat'),
      findsOneWidget,
    );
    StyledDropdown rowDropdown(String key) => tester.widget<StyledDropdown>(
      find.descendant(
        of: find.byKey(ValueKey(key)),
        matching: find.byType(StyledDropdown),
      ),
    );
    expect(rowDropdown('model-effort-name-0').value, 'low');
    expect(rowDropdown('model-effort-name-1').value, 'ultra');
    expect(find.byKey(const ValueKey('model-effort-name-2')), findsNothing);

    await tester.tap(find.widgetWithText(FilledButton, '保存'));
    await tester.pumpAndSettle();
    expect(sentBody, isNotNull);
    expect(sentBody!['efforts'], [
      {'name': 'low', 'value': 'low'},
      {'name': 'ultra', 'value': 'ultra'},
    ]);

    await tester.pump(const Duration(seconds: 3));
    await tester.pumpAndSettle();
  });

  testWidgets(
    '推理档:双端声明(下游 anthropic_adaptive → 上游 anthropic_budget)+关思考落定+预算右列随提交',
    (tester) async {
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

      // 同族约束已撤:双端词表全量罗列,缺省=词表首项 openai_chat;
      // openai_chat 非 anthropic 载档族,关思考落定先隐藏。
      expect(
        find.byKey(const ValueKey('model-effort-off-policy')),
        findsNothing,
      );
      expect(
        tester
            .widget<StyledDropdown>(dropdownIn('model-effort-in-field'))
            .value,
        'openai_chat',
      );
      expect(
        tester
            .widget<StyledDropdown>(dropdownIn('model-effort-format-field'))
            .value,
        'openai_chat',
      );

      // 双端词表全量:下游 6 种(不收 gemini)、上游 8 种,跨族组合合法;
      // effort_index 已废(reasoning_level 只走 auto),双端词表均不收。
      const entryVocab = [
        'openai_chat',
        'openai_responses',
        'anthropic_effort',
        'anthropic_budget',
        'anthropic_adaptive',
        'anthropic_off',
      ];
      const upstreamVocab = [...entryVocab, 'gemini_level', 'gemini_budget'];
      final entryOpts = tester
          .widget<StyledDropdown>(dropdownIn('model-effort-in-field'))
          .options;
      final upstreamOpts = tester
          .widget<StyledDropdown>(dropdownIn('model-effort-format-field'))
          .options;
      expect(entryOpts, entryVocab);
      expect(upstreamOpts, upstreamVocab);
      expect(entryOpts, isNot(contains('')));
      expect(entryOpts, isNot(contains('effort_index')));
      expect(upstreamOpts, isNot(contains('effort_index')));

      // 下游格式 → anthropic_adaptive(跨族改选)。
      await tester.ensureVisible(find.byKey(const ValueKey('model-effort-in')));
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const ValueKey('model-effort-in')));
      await tester.pumpAndSettle();
      await tester.tap(find.text('anthropic_adaptive').last);
      await tester.pumpAndSettle();

      // 上游格式 → anthropic_budget(预算类):关思考落定随即动态出现。
      await tester.ensureVisible(
        find.byKey(const ValueKey('model-effort-format')),
      );
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const ValueKey('model-effort-format')));
      await tester.pumpAndSettle();
      await tester.tap(find.text('anthropic_budget'));
      await tester.pumpAndSettle();
      expect(
        find.byKey(const ValueKey('model-effort-off-policy')),
        findsOneWidget,
      );

      // 关思考落定 → between_tools。
      await tester.ensureVisible(
        find.byKey(const ValueKey('model-effort-off-policy')),
      );
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const ValueKey('model-effort-off-policy')));
      await tester.pumpAndSettle();
      await tester.tap(find.text('between_tools（Sonnet 5.5 顶替 disabled）'));
      await tester.pumpAndSettle();

      // 加一行:左列下游档下拉(默认 low)改选 high,右列预算编辑框填 12000。
      await tester.ensureVisible(
        find.byKey(const ValueKey('model-effort-add')),
      );
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const ValueKey('model-effort-add')));
      await tester.pumpAndSettle();
      await tester.tap(
        find.descendant(
          of: find.byKey(const ValueKey('model-effort-name-0')),
          matching: find.byType(StyledDropdown),
        ),
      );
      await tester.pumpAndSettle();
      await tester.tap(find.text('high').last);
      await tester.pumpAndSettle();
      await tester.enterText(
        find.descendant(
          of: find.byKey(const ValueKey('model-effort-mode-field')),
          matching: find.byType(TextFormField),
        ),
        '12000',
      );
      await tester.pumpAndSettle();

      await tester.ensureVisible(find.byKey(const ValueKey('model-id')));
      await tester.pumpAndSettle();
      await tester.enterText(
        find.byKey(const ValueKey('model-id')),
        'kimi-1/k2',
      );
      await tester.enterText(
        find.byKey(const ValueKey('model-native')),
        'kimi-k2-turbo',
      );
      await tester.tap(find.widgetWithText(FilledButton, '创建'));
      await tester.pumpAndSettle();

      expect(sentBody, isNotNull);
      expect(sentBody!['effort_in'], 'anthropic_adaptive');
      expect(sentBody!['effort_format'], 'anthropic_budget');
      expect(sentBody!['effort_off'], 'between_tools');
      // 预算数直接进映射对右列;预算覆盖表不再从档位行收集(新建=空表)。
      expect(sentBody!['effort_budgets'], isEmpty);
      expect(sentBody!['efforts'], [
        {'name': 'high', 'value': '12000'},
      ]);

      await tester.pump(const Duration(seconds: 3));
      await tester.pumpAndSettle();
    },
  );

  testWidgets('推理档:总开关默认开,关掉后提交 effort_enabled=false', (tester) async {
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

    final sw = find.byKey(const ValueKey('model-effort-enabled'));
    expect(sw, findsOneWidget);
    expect(tester.widget<Switch>(sw).value, isTrue);

    await tester.ensureVisible(sw);
    await tester.pumpAndSettle();
    await tester.tap(sw);
    await tester.pumpAndSettle();
    expect(tester.widget<Switch>(sw).value, isFalse);
    // 关掉总开关不影响其余推理档字段常驻显示。
    expect(find.byKey(const ValueKey('model-effort-in')), findsOneWidget);
    expect(find.byKey(const ValueKey('model-effort-format')), findsOneWidget);

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
    expect(sentBody!['effort_enabled'], isFalse);

    await tester.pump(const Duration(seconds: 3));
    await tester.pumpAndSettle();
  });

  testWidgets('推理档:存量双端字段编辑时回填(入口/关思考/映射对)', (tester) async {
    final editing = UpstreamModel.fromJson(const {
      'id': 'kimi-1/k2',
      'account': 'kimi-1',
      'native_model': 'kimi-k2-turbo',
      'protocol': 'anthropic',
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

    // 上游/关思考下拉回填选中值;存量跨族入口值(openai_chat 与 anthropic
    // 协议跨族)同族约束已撤,原样保留。存量「行号即档号」迁移为映射对:
    // 0/none→none→none、1/high→high→high;预算类上游右列为编辑框,原值
    // 原样呈现(数字串或 none 由后端词表校验把关)。
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
      tester.widget<StyledDropdown>(dropdownIn('model-effort-off-field')).value,
      'omit',
    );
    StyledDropdown rowDropdown(String key) => tester.widget<StyledDropdown>(
      find.descendant(
        of: find.byKey(ValueKey(key)),
        matching: find.byType(StyledDropdown),
      ),
    );
    expect(rowDropdown('model-effort-name-0').value, 'none');
    expect(rowDropdown('model-effort-name-1').value, 'high');
    final rightFields = find.descendant(
      of: find.byKey(const ValueKey('model-effort-mode-field')),
      matching: find.byType(TextFormField),
    );
    expect(rightFields, findsNWidgets(2));
    expect(
      tester.widget<TextFormField>(rightFields.at(0)).initialValue,
      'none',
    );
    expect(
      tester.widget<TextFormField>(rightFields.at(1)).initialValue,
      'high',
    );
  });

  testWidgets('推理档:下游 budget 模型的数字阈值行不做档号迁移', (tester) async {
    // 数字 name 在下游 budget 格式下是预算阈值(新语义),不能被存量
    // 「行号即档号」迁移吃掉(1024→low 之类)——与后端 Effective 的
    // budgetEntry 守卫同口径(沙盒 wire 实测抓到的真问题)。
    final editing = UpstreamModel.fromJson(const {
      'id': 'kimi-1/k2',
      'account': 'kimi-1',
      'native_model': 'kimi-k2-turbo',
      'protocol': 'chat_completions',
      'defaults': <String, dynamic>{},
      'overrides': <String, dynamic>{},
      'efforts': [
        {'name': '1024', 'value': 'low'},
        {'name': '8000', 'value': 'high'},
      ],
      'effort_format': 'openai_responses',
      'effort_in': 'anthropic_budget',
      'effort_off': '',
      'effort_budgets': <String, dynamic>{},
      'enabled': true,
    });
    await pumpForm(tester, editing: editing, client: stubClient());

    // 左列阈值编辑框原值保留(迁移会把 1024 改写成 low)。
    final leftFields = find.descendant(
      of: find.byKey(const ValueKey('model-effort-mode-field')),
      matching: find.byType(TextFormField),
    );
    expect(leftFields, findsNWidgets(2));
    expect(tester.widget<TextFormField>(leftFields.at(0)).initialValue, '1024');
    expect(tester.widget<TextFormField>(leftFields.at(1)).initialValue, '8000');
    // 右列档词表下拉按 value 回填。
    StyledDropdown rowValue(String key) => tester.widget<StyledDropdown>(
      find.descendant(
        of: find.byKey(ValueKey(key)),
        matching: find.byType(StyledDropdown),
      ),
    );
    expect(rowValue('model-effort-value-0').value, 'low');
    expect(rowValue('model-effort-value-1').value, 'high');
  });

  testWidgets('清除配置:存量路径回填、增删行、提交 strip 数组', (tester) async {
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
      'defaults': <String, dynamic>{},
      'overrides': <String, dynamic>{},
      'efforts': [
        {'name': 'high', 'value': 'high'},
      ],
      'strip': ['store', 'metadata.user_id'],
      'enabled': true,
    });
    await pumpForm(tester, editing: editing, client: client);

    // 存量两条路径回填(有值默认展开)。TextFormField 的 decoration 经由
    // 内层 TextField 落地,按 TextField 匹配、controller 文本断言回填值。
    Finder stripFields() => find.byWidgetPredicate(
      (w) =>
          w is TextField && w.decoration?.hintText == '点路径，如 metadata.user_id',
    );
    expect(stripFields(), findsNWidgets(2));
    expect(
      tester.widget<TextField>(stripFields().at(0)).controller?.text,
      'store',
    );
    expect(
      tester.widget<TextField>(stripFields().at(1)).controller?.text,
      'metadata.user_id',
    );

    // 加一行并填入新路径,再删掉首行:剩 metadata.user_id + reasoning.effort。
    await tester.ensureVisible(find.byKey(const ValueKey('model-strip-add')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const ValueKey('model-strip-add')));
    await tester.pumpAndSettle();
    expect(stripFields(), findsNWidgets(3));
    await tester.enterText(stripFields().at(2), 'reasoning.effort');
    await tester.pumpAndSettle();
    await tester.ensureVisible(find.byKey(const ValueKey('model-strip-del-0')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const ValueKey('model-strip-del-0')));
    await tester.pumpAndSettle();
    expect(stripFields(), findsNWidgets(2));

    await tester.tap(find.widgetWithText(FilledButton, '保存'));
    await tester.pumpAndSettle();
    expect(sentBody, isNotNull);
    expect(sentBody!['strip'], ['metadata.user_id', 'reasoning.effort']);

    await tester.pump(const Duration(seconds: 3));
    await tester.pumpAndSettle();
  });

  testWidgets('清除配置:空路径段校验拦截提交', (tester) async {
    var called = false;
    final client = ApiClient(
      baseUrl: 'http://127.0.0.1:8080',
      adminKey: 'adm',
      httpClient: MockClient((req) async {
        called = true;
        return http.Response('{}', 200);
      }),
    );
    final editing = UpstreamModel.fromJson(const {
      'id': 'kimi-1/k2',
      'account': 'kimi-1',
      'native_model': 'kimi-k2-turbo',
      'protocol': 'anthropic',
      'defaults': <String, dynamic>{},
      'overrides': <String, dynamic>{},
      'efforts': [
        {'name': 'high', 'value': 'high'},
      ],
      'strip': ['a..b'],
      'enabled': true,
    });
    await pumpForm(tester, editing: editing, client: client);

    await tester.ensureVisible(find.widgetWithText(FilledButton, '保存'));
    await tester.pumpAndSettle();
    await tester.tap(find.widgetWithText(FilledButton, '保存'));
    await tester.pumpAndSettle();
    expect(find.text('点路径每段不能为空'), findsOneWidget);
    expect(called, isFalse);
  });

  testWidgets('推理档:切协议时双端格式不重置(同族约束已撤)', (tester) async {
    await pumpForm(tester);

    // 缺省:双端词表首项 openai_chat;openai_chat 非 anthropic 载档族,
    // 关思考落定隐藏。
    expect(
      tester.widget<StyledDropdown>(dropdownIn('model-effort-in-field')).value,
      'openai_chat',
    );
    expect(find.byKey(const ValueKey('model-effort-off-policy')), findsNothing);

    // 改上游为 anthropic_budget 后切协议到 chat_completions:
    // 双端值不迁移不重置,关思考落定随 anthropic 族保持显示。
    await tester.ensureVisible(
      find.byKey(const ValueKey('model-effort-format')),
    );
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const ValueKey('model-effort-format')));
    await tester.pumpAndSettle();
    await tester.tap(find.text('anthropic_budget').last);
    await tester.pumpAndSettle();

    await tester.ensureVisible(find.byKey(const ValueKey('protocol-kimi-1')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const ValueKey('protocol-kimi-1')));
    await tester.pumpAndSettle();
    await tester.tap(find.text('chat_completions').last);
    await tester.pumpAndSettle();

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
      find.byKey(const ValueKey('model-effort-off-policy')),
      findsOneWidget,
    );
  });

  testWidgets('推理档:gemini 协议双端字段常显,缺省落词表首项随提交', (tester) async {
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
    final geminiProviders = [
      ProviderSpec.fromJson(const {
        'id': 'gemini.global.api.standard',
        'display_name': 'Gemini',
        'website': 'https://gemini.google.com',
        'base_url': 'https://generativelanguage.googleapis.com',
        'protocols': ['gemini'],
        'auth': 'bearer',
        'credential': 'api_key',
      }),
    ];
    final geminiAccounts = [
      Account.fromJson(const {
        'name': 'g-1',
        'provider_id': 'gemini.global.api.standard',
        'credential': {'kind': 'api_key', 'api_key': 'sk-g***mnop'},
        'base_url': '',
        'headers': <String, dynamic>{},
        'enabled': true,
      }),
    ];
    await pumpForm(
      tester,
      client: client,
      initialAccount: 'g-1',
      accountsOverride: geminiAccounts,
      providersOverride: geminiProviders,
    );

    // 同族约束已撤:gemini 协议下游格式字段也常显,双端缺省都落词表首项
    // openai_chat;openai_chat 非 anthropic 载档族,关思考落定隐藏。
    expect(find.byKey(const ValueKey('model-effort-in-field')), findsOneWidget);
    expect(find.byKey(const ValueKey('model-effort-off-policy')), findsNothing);
    expect(
      tester.widget<StyledDropdown>(dropdownIn('model-effort-in-field')).value,
      'openai_chat',
    );
    expect(
      tester
          .widget<StyledDropdown>(dropdownIn('model-effort-format-field'))
          .value,
      'openai_chat',
    );
    expect(
      find.textContaining('下游 openai_chat → 上游 openai_chat'),
      findsOneWidget,
    );

    await tester.enterText(find.byKey(const ValueKey('model-id')), 'g-1/gm');
    await tester.enterText(
      find.byKey(const ValueKey('model-native')),
      'gemini-3-pro',
    );
    await tester.tap(find.widgetWithText(FilledButton, '创建'));
    await tester.pumpAndSettle();

    expect(sentBody, isNotNull);
    expect(sentBody!['protocol'], 'gemini');
    expect(sentBody!['effort_in'], 'openai_chat');
    expect(sentBody!['effort_format'], 'openai_chat');

    await tester.pump(const Duration(seconds: 3));
    await tester.pumpAndSettle();
  });

  testWidgets('推理档:无档位类上游(anthropic_off)隐藏映射表与关思考落定,提交空声明', (tester) async {
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

    // 缺省 openai_chat(effort 类):映射表在;非 anthropic 载档族,关思考落定
    // 隐藏。加一行(默认 low→low)备用。
    expect(
      find.byKey(const ValueKey('model-effort-mode-field')),
      findsOneWidget,
    );
    expect(find.byKey(const ValueKey('model-effort-off-policy')), findsNothing);
    await tester.ensureVisible(find.byKey(const ValueKey('model-effort-add')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const ValueKey('model-effort-add')));
    await tester.pumpAndSettle();

    // 切到无档位类:映射表/关思考落定全隐,说明文案出现;
    // 下游格式保留(读档剥键仍需要);副标题显「无档位」。
    await tester.ensureVisible(
      find.byKey(const ValueKey('model-effort-format')),
    );
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const ValueKey('model-effort-format')));
    await tester.pumpAndSettle();
    await tester.tap(find.text('anthropic_off'));
    await tester.pumpAndSettle();
    expect(find.byKey(const ValueKey('model-effort-mode-field')), findsNothing);
    expect(find.byKey(const ValueKey('model-effort-off-policy')), findsNothing);
    expect(find.byKey(const ValueKey('model-effort-in-field')), findsOneWidget);
    expect(find.textContaining('无档位'), findsWidgets);

    // 切回载档类:映射表复现且已填行保留(low 在两端词表内,不矫正)。
    await tester.tap(find.byKey(const ValueKey('model-effort-format')));
    await tester.pumpAndSettle();
    await tester.tap(find.text('anthropic_effort').last);
    await tester.pumpAndSettle();
    expect(
      find.byKey(const ValueKey('model-effort-mode-field')),
      findsOneWidget,
    );
    expect(
      tester
          .widget<StyledDropdown>(
            find.descendant(
              of: find.byKey(const ValueKey('model-effort-value-0')),
              matching: find.byType(StyledDropdown),
            ),
          )
          .value,
      'low',
    );

    // 再切回无档位类保存:efforts 落空声明,effort_off 不落键值。
    await tester.tap(find.byKey(const ValueKey('model-effort-format')));
    await tester.pumpAndSettle();
    await tester.tap(find.text('anthropic_off'));
    await tester.pumpAndSettle();
    await tester.enterText(find.byKey(const ValueKey('model-id')), 'kimi-1/nt');
    await tester.enterText(
      find.byKey(const ValueKey('model-native')),
      'kimi-nonthinking',
    );
    await tester.tap(find.widgetWithText(FilledButton, '创建'));
    await tester.pumpAndSettle();

    expect(sentBody, isNotNull);
    expect(sentBody!['effort_format'], 'anthropic_off');
    expect(sentBody!['efforts'], isEmpty);

    await tester.pump(const Duration(seconds: 3));
    await tester.pumpAndSettle();
  });
}

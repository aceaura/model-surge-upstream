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
    'id': 'kimi',
    'display_name': 'Moonshot Kimi',
    'website': 'https://platform.moonshot.cn',
    'base_url': 'https://api.kimi.com/coding',
    'protocols': ['anthropic', 'chat_completions'],
    'auth': 'anthropic_key',
    'credential': 'api_key',
  }),
  ProviderSpec.fromJson(const {
    'id': 'openai',
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
    'provider_id': 'kimi',
    'credential': {'kind': 'api_key', 'api_key': 'sk-l***efgh'},
    'base_url': '',
    'headers': <String, dynamic>{},
    'enabled': true,
  }),
  Account.fromJson(const {
    'name': 'oa-1',
    'provider_id': 'openai',
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
  await tester.pumpWidget(MaterialApp(
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
  ));
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
  testWidgets('protocol options come from the selected account provider',
      (tester) async {
    await pumpForm(tester);

    await tester.tap(dropdownIn('model-protocol-field'));
    await tester.pumpAndSettle();
    // kimi 支持 anthropic 与 chat_completions，不支持 responses。
    expect(find.text('anthropic'), findsWidgets);
    expect(find.text('responses'), findsNothing);

    await tester.tap(find.text('anthropic').last);
    await tester.pumpAndSettle();
  });

  testWidgets('switching account refreshes the protocol options',
      (tester) async {
    await pumpForm(tester);

    await tester.tap(dropdownIn('model-account-field'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('oa-1 (openai)').last);
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
    expect(tester.widget<FilledButton>(submit).onPressed, isNull,
        reason: 'invalid json must block submission');

    await tester.enterText(
        jsonBox('model-defaults'), '{"temperature":0.6}');
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
    // 编辑态不渲染标识字段(标题已含标识,且不可改)。
    expect(find.byKey(const ValueKey('model-id')), findsNothing);
  });

  testWidgets('copy prefills config but keeps create semantics',
      (tester) async {
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

    expect(find.text('拷贝模型 kimi-1/k2'), findsOneWidget);
    // 标识加 -copy 后缀,且标识字段可编辑(新建语义)
    final idField =
        tester.widget<TextFormField>(find.byKey(const ValueKey('model-id')));
    expect(idField.controller!.text, 'kimi-1/k2-copy');
    expect(find.text('kimi-k2-turbo'), findsAtLeastNWidgets(1));
    expect(find.text('262.144'), findsAtLeastNWidgets(1),
        reason: '上下文窗口随源模型预填');
    expect(find.textContaining('"temperature": 0.6'), findsAtLeastNWidgets(1));
    expect(find.textContaining('"max_tokens": 8192'), findsAtLeastNWidgets(1));
    expect(find.text('创建'), findsOneWidget,
        reason: 'copy is a create, not an edit');
  });

  testWidgets('form groups fields into three collapsible sections',
      (tester) async {
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
    expect(find.text('kimi-1 · anthropic · kimi-k2-turbo'), findsOneWidget,
        reason: '收起也能靠副标题辨认基本信息');
    expect(find.text('窗口 262.144k · 元数据'), findsOneWidget);
    expect(find.textContaining('"temperature": 0.6'), findsAtLeastNWidgets(1),
        reason: '已配参数时附加参数分栏默认展开');

    // 折叠再展开,已填内容不丢(分栏只裁剪不卸载)。
    await tester.tap(find.text('基本信息'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('基本信息'));
    await tester.pumpAndSettle();
    expect(find.text('kimi-k2-turbo'), findsAtLeastNWidgets(1));
  });

  testWidgets('params section expands on demand for empty config',
      (tester) async {
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

  testWidgets('context window is submitted in tokens from k input',
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

    await tester.enterText(
        find.byKey(const ValueKey('model-id')), 'kimi-1/k2');
    await tester.enterText(
        find.byKey(const ValueKey('model-native')), 'kimi-k2-turbo');
    // 256k 应以 256000 tokens 提交
    await tester.enterText(
        find.byKey(const ValueKey('model-context')), '256');
    await tester.tap(find.widgetWithText(FilledButton, '创建'));
    await tester.pumpAndSettle();

    expect(sentBody, isNotNull);
    expect(sentBody!['context_window'], 256000);

    // 冲刷提示框的 2.4s 驻留定时器与滑出动画,避免遗留 Timer。
    await tester.pump(const Duration(seconds: 3));
    await tester.pumpAndSettle();
  });

  testWidgets('推理档:新建默认自动,自定义勾选按词表序随提交上行',
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

    // 新建默认自动:分区收起,副标题标注自动推导。
    expect(find.text('推理档'), findsOneWidget);
    expect(find.text('自动（按协议与模型名推导）'), findsOneWidget);

    // 展开并切到自定义:七个档位的 chip 全渲染(分区在首屏外,先滚入视野)。
    await tester.tap(find.text('推理档'));
    await tester.pumpAndSettle();
    await tester.ensureVisible(dropdownIn('model-effort-mode-field'));
    await tester.pumpAndSettle();
    await tester.tap(dropdownIn('model-effort-mode-field'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('自定义').last);
    await tester.pumpAndSettle();
    expect(find.byKey(const ValueKey('model-effort-none')), findsOneWidget);
    expect(find.byKey(const ValueKey('model-effort-max')), findsOneWidget);

    // 勾选 高/低(乱序点选),提交应去重并按词表升序。
    await tester.tap(find.byKey(const ValueKey('model-effort-high')));
    await tester.pump();
    await tester.tap(find.byKey(const ValueKey('model-effort-low')));
    await tester.pump();

    await tester.ensureVisible(find.byKey(const ValueKey('model-id')));
    await tester.pumpAndSettle();
    await tester.enterText(
        find.byKey(const ValueKey('model-id')), 'kimi-1/k2');
    await tester.enterText(
        find.byKey(const ValueKey('model-native')), 'kimi-k2-turbo');
    await tester.tap(find.widgetWithText(FilledButton, '创建'));
    await tester.pumpAndSettle();

    expect(sentBody, isNotNull);
    expect(sentBody!['efforts'], ['low', 'high']);

    await tester.pump(const Duration(seconds: 3));
    await tester.pumpAndSettle();
  });

  testWidgets('推理档:编辑回显显式配置,切回自动提交 null', (tester) async {
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
      'efforts': ['low', 'high'],
      'efforts_effective': ['low', 'high'],
      'enabled': true,
    });
    await pumpForm(tester, editing: editing, client: client);

    // 显式配置:分区默认展开,副标题列出档位,chips 选中态一致。
    expect(find.text('低 / 高'), findsOneWidget);
    expect(
        tester
            .widget<FilterChip>(find.byKey(const ValueKey('model-effort-low')))
            .selected,
        isTrue);
    expect(
        tester
            .widget<FilterChip>(
                find.byKey(const ValueKey('model-effort-medium')))
            .selected,
        isFalse);

    // 切回自动并保存:efforts 键仍在但为 null(恢复服务端推导)。
    await tester.ensureVisible(dropdownIn('model-effort-mode-field'));
    await tester.pumpAndSettle();
    await tester.tap(dropdownIn('model-effort-mode-field'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('自动').last);
    await tester.pumpAndSettle();
    await tester.tap(find.widgetWithText(FilledButton, '保存'));
    await tester.pumpAndSettle();

    expect(sentBody, isNotNull);
    expect(sentBody!.containsKey('efforts'), isTrue);
    expect(sentBody!['efforts'], isNull);

    await tester.pump(const Duration(seconds: 3));
    await tester.pumpAndSettle();
  });
}

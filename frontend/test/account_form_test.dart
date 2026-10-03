import 'dart:convert';
import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:msu_admin/api_client.dart';
import 'package:msu_admin/models.dart';
import 'package:msu_admin/pages/account_form.dart';
import 'package:msu_admin/theme.dart';

final providers = [
  ProviderSpec.fromJson(const {
    'id': 'deepseek',
    'display_name': 'DeepSeek',
    'website': 'https://platform.deepseek.com',
    'base_url': 'https://api.deepseek.com',
    'protocols': ['chat_completions'],
    'auth': 'bearer',
    'credential': 'api_key',
    'billing': 'paygo',
    'region': 'Global',
  }),
  ProviderSpec.fromJson(const {
    'id': 'openai',
    'display_name': 'OpenAI',
    'website': 'https://openai.com',
    'base_url': 'https://api.openai.com',
    'protocols': ['chat_completions', 'responses'],
    'auth': 'bearer',
    'credential': 'api_key',
    'billing': 'paygo',
    'region': 'Global',
  }),
  ProviderSpec.fromJson(const {
    'id': 'openai-codex',
    'display_name': 'OpenAI',
    'website': 'https://chatgpt.com',
    'base_url': 'https://chatgpt.com/backend-api/codex',
    'protocols': ['responses'],
    'auth': 'bearer',
    'credential': 'oauth_refresh',
    'billing': 'subscription',
    'region': 'Global',
  }),
];

/// oauth_refresh(订阅登录态)账号样本。
final accountOAuth = Account.fromJson(const {
  'name': 'gpt-1',
  'provider_id': 'openai-codex',
  'credential': {
    'kind': 'oauth_refresh',
    'refresh_token': 'rt-a***z',
    'account_id': 'acc-123',
  },
  'base_url': '',
  'enabled': true,
});

final account = Account.fromJson(const {
  'name': 'ds-1',
  'provider_id': 'deepseek',
  'credential': {'kind': 'api_key', 'api_key': 'sk-d***efgh'},
  'base_url': 'https://ds.example.com',
  'headers': <String, dynamic>{'x-tenant': 'a'},
  'enabled': false,
});

/// 无 base_url 的账号:拷贝/编辑时请求地址应回落到提供商默认值。
final accountNoOverride = Account.fromJson(const {
  'name': 'ds-2',
  'provider_id': 'deepseek',
  'credential': {'kind': 'api_key', 'api_key': 'sk-x***y'},
  'base_url': '',
  'enabled': true,
});

/// 带额度脚本的账号:脚本区预填与清除语义的样本。
final accountWithScript = Account.fromJson(const {
  'name': 'ds-3',
  'provider_id': 'deepseek',
  'credential': {'kind': 'api_key', 'api_key': 'sk-s***t'},
  'base_url': '',
  'enabled': true,
  'quota_script': {
    'enabled': true,
    'code': '({request: {url: "{{baseUrl}}/x"}, extractor: function(r) { return {remaining: r.b, unit: "USD"}; }})',
    'timeout_seconds': 15,
    'auto_interval_minutes': 5,
  },
});

ApiClient stubClient() => ApiClient(
      baseUrl: 'http://127.0.0.1:8080',
      adminKey: 'adm',
      httpClient: MockClient((_) async => http.Response('{}', 200)),
    );

/// 记录请求 body 的客户端,用于断言提交时 base_url 的实际取值。
ApiClient recordingClient(List<String> captured) => ApiClient(
      baseUrl: 'http://127.0.0.1:8080',
      adminKey: 'adm',
      httpClient: MockClient((req) async {
        captured.add(req.body);
        return http.Response(
            jsonEncode({
              'account': {
                'name': 'a',
                'provider_id': 'deepseek',
                'credential': {'kind': 'api_key', 'api_key': 'k'},
                'base_url': '',
                'enabled': true,
              }
            }),
            200);
      }),
    );

/// 表单已是整页路由组件,直接作为 home pump。
Future<void> pumpForm(
  WidgetTester tester, {
  Account? copyFrom,
  Account? editing,
  ApiClient? client,
}) async {
  tester.view.physicalSize = const Size(1200, 900);
  tester.view.devicePixelRatio = 1.0;
  addTearDown(tester.view.reset);
  await tester.pumpWidget(MaterialApp(
    theme: buildAppTheme(),
    home: AccountForm(
        client: client ?? stubClient(),
        providers: providers,
        onDone: (_) {},
        editing: editing,
        copyFrom: copyFrom),
  ));
  await tester.pumpAndSettle();
}

/// 额度脚本区默认折叠,展开并滚入视野。
Future<void> expandScriptSection(WidgetTester tester) async {
  final header = find.text('额度脚本');
  await tester.ensureVisible(header);
  await tester.tap(header);
  await tester.pumpAndSettle();
  await tester.ensureVisible(find.byKey(const ValueKey('quota-script-code')));
  await tester.pumpAndSettle();
}

String fieldText(WidgetTester tester, String key) => tester
    .widget<TextFormField>(find.byKey(ValueKey(key)))
    .controller!
    .text;

/// 三级级联从上至下逐级点选(厂商→计费模式→服务区域)。
Future<void> selectCascade(
  WidgetTester tester, {
  required String vendor,
  required String billing,
  required String region,
}) async {
  await tester.tap(find.byKey(const ValueKey('account-provider-vendor')));
  await tester.pumpAndSettle();
  await tester.tap(find.text(vendor).last);
  await tester.pumpAndSettle();
  await tester.tap(find.byKey(const ValueKey('account-provider-billing')));
  await tester.pumpAndSettle();
  await tester.tap(find.text(billing).last);
  await tester.pumpAndSettle();
  await tester.tap(find.byKey(const ValueKey('account-provider-region')));
  await tester.pumpAndSettle();
  await tester.tap(find.text(region).last);
  await tester.pumpAndSettle();
}

void main() {
  testWidgets('copy prefills config but keeps create semantics', (tester) async {
    await pumpForm(tester, copyFrom: account);

    expect(find.text('拷贝账号 ds-1'), findsOneWidget);
    expect(find.text('DeepSeek'), findsNWidgets(2),
        reason: '厂商级跟随来源账号反推预填(下拉选中值+分栏副标题各一处)');
    expect(find.text('按量计费'), findsOneWidget, reason: '计费模式级预填');
    expect(find.text('全球'), findsOneWidget, reason: '服务区域级预填');
    expect(fieldText(tester, 'account-name'), 'ds-1-copy');
    expect(fieldText(tester, 'account-base-url'), 'https://ds.example.com',
        reason: '已存覆盖值直接预填');
    expect(find.textContaining('************'), findsOneWidget,
        reason: '默认纯星号,不泄露掩码里的任何字符');
    expect(find.byType(SwitchListTile), findsNothing,
        reason: '启停由列表行开关控制,表单不再展示');
    expect(find.text('创建'), findsOneWidget,
        reason: 'copy is a create, not an edit');
  });

  testWidgets('basic info section defaults expanded with config subtitle',
      (tester) async {
    await pumpForm(tester, editing: account);

    expect(find.text('基本信息'), findsOneWidget);
    expect(find.text('DeepSeek'), findsWidgets,
        reason: '副标题与厂商下拉选中值都只留厂商名');
    expect(find.text('DeepSeek · https://ds.example.com'), findsNothing,
        reason: '基本信息副标题不再拼接具体访问地址');
    expect(find.byKey(const ValueKey('account-api-key')), findsOneWidget,
        reason: '主信息默认展开,字段直接可见');
    expect(fieldText(tester, 'account-name'), 'ds-1');
    expect(
        tester
            .widget<TextFormField>(find.byKey(const ValueKey('account-name')))
            .enabled,
        isFalse,
        reason: '编辑态账号名灰框禁用展示,资源键不可改');

    // 折叠再展开,已填内容不丢(分栏只裁剪不卸载)。
    await tester.tap(find.text('基本信息'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('基本信息'));
    await tester.pumpAndSettle();
    expect(fieldText(tester, 'account-base-url'), 'https://ds.example.com');
  });

  testWidgets('disabled name field border matches enabled field style',
      (tester) async {
    await pumpForm(tester, editing: account);

    InputDecoration decoOf(String key) => tester
        .widget<InputDecorator>(find
            .descendant(
                of: find.byKey(ValueKey(key)),
                matching: find.byType(InputDecorator))
            .first)
        .decoration;

    final name = decoOf('account-name');
    final url = decoOf('account-base-url');
    final disabled = name.disabledBorder as OutlineInputBorder;
    final enabled = url.enabledBorder as OutlineInputBorder;
    expect(disabled.borderSide, enabled.borderSide,
        reason: '禁用框描边颜色/宽度与启用框一致');
    expect(disabled.borderRadius, enabled.borderRadius, reason: '圆角一致');
    expect(name.fillColor, url.fillColor, reason: '填充色一致');
  });

  testWidgets('edit masks key hint behind asterisks until eye tapped',
      (tester) async {
    await pumpForm(tester, editing: account);

    expect(find.textContaining('************'), findsOneWidget);
    expect(find.textContaining('sk-d***efgh'), findsNothing,
        reason: '未点眼睛前掩码字符一个都不露');

    await tester.tap(find.byTooltip('显示'));
    await tester.pump();
    expect(find.textContaining('sk-d***efgh'), findsOneWidget);
    expect(find.textContaining('************'), findsNothing);

    await tester.tap(find.byTooltip('隐藏'));
    await tester.pump();
    expect(find.textContaining('************'), findsOneWidget);
  });

  testWidgets('copy falls back to provider default when no override',
      (tester) async {
    await pumpForm(tester, copyFrom: accountNoOverride);
    expect(fieldText(tester, 'account-base-url'), 'https://api.deepseek.com');
  });

  testWidgets('plain create stays blank until cascade selected', (tester) async {
    await pumpForm(tester);
    expect(find.text('新建账号'), findsOneWidget);
    expect(fieldText(tester, 'account-name'), isEmpty);
    expect(fieldText(tester, 'account-base-url'), isEmpty,
        reason: '三级级联未选定前没有提供商,地址留空');

    await selectCascade(tester, vendor: 'DeepSeek', billing: '按量计费', region: '全球');
    expect(fieldText(tester, 'account-base-url'), 'https://api.deepseek.com',
        reason: '三级选定后自动带出该提供商的默认请求地址');
  });

  testWidgets('lower cascade levels stay disabled until upper chosen',
      (tester) async {
    await pumpForm(tester);

    await tester.tap(find.byKey(const ValueKey('account-provider-billing')));
    await tester.pumpAndSettle();
    expect(find.text('按量计费'), findsNothing,
        reason: '厂商未选,计费模式禁用点开不出菜单');

    await tester.tap(find.byKey(const ValueKey('account-provider-vendor')));
    await tester.pumpAndSettle();
    await tester.tap(find.text('DeepSeek').last);
    await tester.pumpAndSettle();

    await tester.tap(find.byKey(const ValueKey('account-provider-region')));
    await tester.pumpAndSettle();
    expect(find.text('全球'), findsNothing,
        reason: '计费模式未选,服务区域禁用点开不出菜单');
  });

  testWidgets('switching vendor resets lower levels and followed url',
      (tester) async {
    await pumpForm(tester);
    await selectCascade(tester, vendor: 'DeepSeek', billing: '按量计费', region: '全球');
    expect(fieldText(tester, 'account-base-url'), 'https://api.deepseek.com');

    await tester.tap(find.byKey(const ValueKey('account-provider-vendor')));
    await tester.pumpAndSettle();
    await tester.tap(find.text('OpenAI').last);
    await tester.pumpAndSettle();

    expect(find.text('按量计费'), findsNothing, reason: '换厂商后计费模式被重置');
    expect(find.text('全球'), findsNothing, reason: '换厂商后服务区域被重置');
    expect(fieldText(tester, 'account-base-url'), isEmpty,
        reason: '地址仍跟随默认值,提供商未解析时清空');
  });

  testWidgets('switching provider swaps an untouched default', (tester) async {
    await pumpForm(tester);

    await selectCascade(tester, vendor: 'OpenAI', billing: '按量计费', region: '全球');

    expect(fieldText(tester, 'account-base-url'), 'https://api.openai.com',
        reason: '用户没改过地址,选定后跟着带出默认值');
  });

  testWidgets('switching provider preserves a customized url', (tester) async {
    await pumpForm(tester);

    await tester.enterText(
        find.byKey(const ValueKey('account-base-url')),
        'https://my-gateway.example.com');
    await tester.pumpAndSettle();

    await selectCascade(tester, vendor: 'OpenAI', billing: '按量计费', region: '全球');

    expect(fieldText(tester, 'account-base-url'),
        'https://my-gateway.example.com');
  });

  testWidgets('submit sends empty override when url equals provider default',
      (tester) async {
    final captured = <String>[];
    await pumpForm(tester, client: recordingClient(captured));

    await selectCascade(tester, vendor: 'DeepSeek', billing: '按量计费', region: '全球');
    await tester.enterText(find.byKey(const ValueKey('account-name')), 'ds-new');
    await tester.enterText(
        find.byKey(const ValueKey('account-api-key')), 'sk-test');
    await tester.tap(find.widgetWithText(FilledButton, '创建'));
    await tester.pumpAndSettle();

    expect(captured, hasLength(1));
    final body = jsonDecode(captured.single) as Map<String, dynamic>;
    expect(body['provider_id'], 'deepseek', reason: '三级级联最终解析回 provider id');
    expect(body['base_url'], '',
        reason: '与提供商默认值相同→空覆盖,继续跟随提供商');
  });

  testWidgets('submit sends the customized url as override', (tester) async {
    final captured = <String>[];
    await pumpForm(tester, client: recordingClient(captured));

    await selectCascade(tester, vendor: 'DeepSeek', billing: '按量计费', region: '全球');
    await tester.enterText(find.byKey(const ValueKey('account-name')), 'ds-new');
    await tester.enterText(
        find.byKey(const ValueKey('account-api-key')), 'sk-test');
    await tester.enterText(find.byKey(const ValueKey('account-base-url')),
        'https://my-gateway.example.com');
    await tester.tap(find.widgetWithText(FilledButton, '创建'));
    await tester.pumpAndSettle();

    expect(captured, hasLength(1));
    final body = jsonDecode(captured.single) as Map<String, dynamic>;
    expect(body['base_url'], 'https://my-gateway.example.com');
  });

  testWidgets('url without scheme fails validation', (tester) async {
    final captured = <String>[];
    await pumpForm(tester, client: recordingClient(captured));

    await selectCascade(tester, vendor: 'DeepSeek', billing: '按量计费', region: '全球');
    await tester.enterText(find.byKey(const ValueKey('account-name')), 'ds-new');
    await tester.enterText(
        find.byKey(const ValueKey('account-api-key')), 'sk-test');
    await tester.enterText(find.byKey(const ValueKey('account-base-url')),
        'api.deepseek.com');
    await tester.tap(find.widgetWithText(FilledButton, '创建'));
    await tester.pumpAndSettle();

    expect(captured, isEmpty, reason: '校验失败不发请求');
    expect(find.text('需以 http:// 或 https:// 开头'), findsOneWidget);
  });

  testWidgets('copy prefills quota script and create submits it',
      (tester) async {
    final captured = <String>[];
    await pumpForm(tester,
        copyFrom: accountWithScript, client: recordingClient(captured));

    // 启用中的脚本让分栏初始展开,代码与数值直接回显
    expect(find.byKey(const ValueKey('quota-script-code')), findsOneWidget);
    expect(fieldText(tester, 'quota-script-code'),
        accountWithScript.quotaScript!.code);
    expect(fieldText(tester, 'quota-script-timeout'), '15');
    expect(fieldText(tester, 'quota-script-interval'), '5');
    expect(
        tester
            .widget<Switch>(find.byKey(const ValueKey('quota-script-enabled')))
            .value,
        isTrue);

    await tester.ensureVisible(find.byKey(const ValueKey('account-name')));
    await tester.enterText(find.byKey(const ValueKey('account-name')), 'ds-new');
    await tester.enterText(
        find.byKey(const ValueKey('account-api-key')), 'sk-test');
    await tester.tap(find.widgetWithText(FilledButton, '创建'));
    await tester.pumpAndSettle();

    final body = jsonDecode(captured.single) as Map<String, dynamic>;
    final script = body['quota_script'] as Map<String, dynamic>;
    expect(script['enabled'], isTrue);
    expect(script['code'], accountWithScript.quotaScript!.code);
    expect(script['timeout_seconds'], 15);
    expect(script['auto_interval_minutes'], 5);
  });

  testWidgets('create without touching script section omits the key',
      (tester) async {
    final captured = <String>[];
    await pumpForm(tester, client: recordingClient(captured));

    await selectCascade(tester, vendor: 'DeepSeek', billing: '按量计费', region: '全球');
    await tester.enterText(find.byKey(const ValueKey('account-name')), 'ds-new');
    await tester.enterText(
        find.byKey(const ValueKey('account-api-key')), 'sk-test');
    await tester.tap(find.widgetWithText(FilledButton, '创建'));
    await tester.pumpAndSettle();

    final body = jsonDecode(captured.single) as Map<String, dynamic>;
    expect(body.containsKey('quota_script'), isFalse,
        reason: '没配脚本就不发该字段,服务端保持未配置');
  });

  testWidgets('edit clearing the script sends an explicit empty object',
      (tester) async {
    final captured = <String>[];
    await pumpForm(tester,
        editing: accountWithScript, client: recordingClient(captured));

    // 初始展开(脚本启用中):关掉开关并清空代码=不要脚本了
    await tester.ensureVisible(find.byKey(const ValueKey('quota-script-enabled')));
    await tester.tap(find.byKey(const ValueKey('quota-script-enabled')));
    await tester.pumpAndSettle();
    await tester.enterText(find.byKey(const ValueKey('quota-script-code')), '');
    await tester.pumpAndSettle();

    await tester.ensureVisible(find.widgetWithText(FilledButton, '保存'));
    await tester.tap(find.widgetWithText(FilledButton, '保存'));
    await tester.pumpAndSettle();

    final body = jsonDecode(captured.single) as Map<String, dynamic>;
    expect(body['quota_script'], isA<Map<String, dynamic>>().having(
        (m) => m.isEmpty, 'isEmpty', isTrue),
        reason: '原来有配置时清空要显式发空对象,服务端才清除而不是保留');
  });

  testWidgets('enabled script with empty code fails validation',
      (tester) async {
    final captured = <String>[];
    await pumpForm(tester,
        editing: accountWithScript, client: recordingClient(captured));

    await tester.enterText(find.byKey(const ValueKey('quota-script-code')), '');
    await tester.pumpAndSettle();
    await tester.ensureVisible(find.widgetWithText(FilledButton, '保存'));
    await tester.tap(find.widgetWithText(FilledButton, '保存'));
    await tester.pumpAndSettle();

    expect(captured, isEmpty, reason: '校验失败不发请求');
    expect(find.text('启用脚本时代码不能为空'), findsOneWidget);
  });

  testWidgets('test button hits quota-test endpoint and shows result',
      (tester) async {
    String? hitPath;
    String? hitBody;
    final client = ApiClient(
      baseUrl: 'http://127.0.0.1:8080',
      adminKey: 'adm',
      httpClient: MockClient((req) async {
        hitPath = req.url.path;
        hitBody = req.body;
        return http.Response(
            jsonEncode({
              'ok': true,
              'report': {
                'account': 'ds-3',
                'queryable': true,
                'meters': [
                  {
                    'kind': 'balance',
                    'unit': 'currency',
                    'currency': 'USD',
                    'remaining': 9.5,
                  },
                ],
              },
            }),
            200,
            headers: {'content-type': 'application/json'});
      }),
    );
    await pumpForm(tester, editing: accountWithScript, client: client);

    await tester.ensureVisible(find.byKey(const ValueKey('quota-script-test')));
    await tester.tap(find.byKey(const ValueKey('quota-script-test')));
    await tester.pumpAndSettle();

    expect(hitPath, '/admin/accounts/ds-3/quota-test');
    expect(
        (jsonDecode(hitBody!) as Map<String, dynamic>)['code'],
        accountWithScript.quotaScript!.code);
    expect(find.textContaining('试跑成功'), findsOneWidget);
  });

  testWidgets('create mode disables the test button', (tester) async {
    await pumpForm(tester);
    await expandScriptSection(tester);

    final button = tester.widget<OutlinedButton>(
        find.widgetWithText(OutlinedButton, '试跑脚本'));
    expect(button.onPressed, isNull,
        reason: '试跑凭据取自已存账号,新建态不可试跑');
    expect(find.text('保存账号后才能试跑'), findsOneWidget);
  });

  // ── oauth_refresh(订阅登录态)表单分流 ──

  testWidgets('oauth provider shows login fields instead of api key',
      (tester) async {
    await pumpForm(tester);

    await selectCascade(tester, vendor: 'OpenAI', billing: '订阅', region: '全球');

    expect(find.byKey(const ValueKey('account-api-key')), findsNothing,
        reason: '订阅登录态不是静态密钥,密钥框不出现');
    expect(find.byKey(const ValueKey('account-refresh-token')), findsOneWidget);
    expect(find.byKey(const ValueKey('account-account-id')), findsOneWidget);
    expect(fieldText(tester, 'account-base-url'),
        'https://chatgpt.com/backend-api/codex',
        reason: '级联解析到 openai-codex 后带出其默认地址');
  });

  testWidgets('oauth create submits credential object not api_key',
      (tester) async {
    final captured = <String>[];
    await pumpForm(tester, client: recordingClient(captured));

    await selectCascade(tester, vendor: 'OpenAI', billing: '订阅', region: '全球');
    await tester.enterText(find.byKey(const ValueKey('account-name')), 'gpt-1');
    await tester.enterText(
        find.byKey(const ValueKey('account-refresh-token')), 'rt-live');
    await tester.enterText(
        find.byKey(const ValueKey('account-account-id')), 'acc-9');
    await tester.tap(find.widgetWithText(FilledButton, '创建'));
    await tester.pumpAndSettle();

    expect(captured, hasLength(1));
    final body = jsonDecode(captured.single) as Map<String, dynamic>;
    expect(body['provider_id'], 'openai-codex');
    expect(body.containsKey('api_key'), isFalse,
        reason: 'oauth 形态不走 api_key 简写');
    final cred = body['credential'] as Map<String, dynamic>;
    expect(cred, {
      'kind': 'oauth_refresh',
      'refresh_token': 'rt-live',
      'account_id': 'acc-9',
    });
  });

  testWidgets('oauth create requires refresh token', (tester) async {
    final captured = <String>[];
    await pumpForm(tester, client: recordingClient(captured));

    await selectCascade(tester, vendor: 'OpenAI', billing: '订阅', region: '全球');
    await tester.enterText(find.byKey(const ValueKey('account-name')), 'gpt-1');
    await tester.enterText(
        find.byKey(const ValueKey('account-account-id')), 'acc-9');
    await tester.tap(find.widgetWithText(FilledButton, '创建'));
    await tester.pumpAndSettle();

    expect(captured, isEmpty, reason: '校验失败不发请求');
    expect(find.text('Refresh Token 不能为空'), findsOneWidget);
  });

  testWidgets('oauth edit prefills account id and keeps credential when blank',
      (tester) async {
    final captured = <String>[];
    await pumpForm(tester, editing: accountOAuth, client: recordingClient(captured));

    expect(fieldText(tester, 'account-account-id'), 'acc-123',
        reason: 'account_id 是标识不是秘密,预填回显');
    expect(find.textContaining('************'), findsOneWidget,
        reason: 'refresh_token 默认纯星号');
    expect(find.byKey(const ValueKey('oauth-reauth-banner')), findsNothing,
        reason: '未标记失效不出横幅');

    await tester.ensureVisible(find.widgetWithText(FilledButton, '保存'));
    await tester.tap(find.widgetWithText(FilledButton, '保存'));
    await tester.pumpAndSettle();

    final body = jsonDecode(captured.single) as Map<String, dynamic>;
    expect(body.containsKey('credential'), isFalse,
        reason: 'refresh_token 留空=保留原登录态,整体不动凭据');
  });

  testWidgets('oauth edit replaces credential when refresh token pasted',
      (tester) async {
    final captured = <String>[];
    await pumpForm(tester, editing: accountOAuth, client: recordingClient(captured));

    await tester.enterText(
        find.byKey(const ValueKey('account-refresh-token')), 'rt-new');
    await tester.ensureVisible(find.widgetWithText(FilledButton, '保存'));
    await tester.tap(find.widgetWithText(FilledButton, '保存'));
    await tester.pumpAndSettle();

    final body = jsonDecode(captured.single) as Map<String, dynamic>;
    final cred = body['credential'] as Map<String, dynamic>;
    expect(cred['kind'], 'oauth_refresh');
    expect(cred['refresh_token'], 'rt-new');
    expect(cred['account_id'], 'acc-123', reason: '预填的 account_id 随凭据一起提交');
  });

  testWidgets('oauth edit shows reauth banner when flagged', (tester) async {
    final flagged = Account.fromJson(const {
      'name': 'gpt-1',
      'provider_id': 'openai-codex',
      'credential': {
        'kind': 'oauth_refresh',
        'refresh_token': 'rt-a***z',
        'account_id': 'acc-123',
      },
      'base_url': '',
      'enabled': true,
      'needs_reauth': true,
    });
    await pumpForm(tester, editing: flagged);

    expect(find.byKey(const ValueKey('oauth-reauth-banner')), findsOneWidget,
        reason: '服务端标记登录态终态失效时提示重新粘贴');
  });

  // ── 本机登录态一键填入(codex CLI auth.json / CC Switch 保管库)──

  /// 建一个临时用户目录并覆写探测根,返回可写相对路径的辅助函数。
  String Function(String) useTempHome() {
    final dir = Directory.systemTemp.createTempSync('msu-auth-test');
    addTearDown(() {
      debugCodexHomeOverride = null;
      dir.deleteSync(recursive: true);
    });
    debugCodexHomeOverride = dir.path;
    return (rel) {
      final file = File('${dir.path}${Platform.pathSeparator}$rel');
      file.parent.createSync(recursive: true);
      return file.path;
    };
  }

  Future<void> tapAutofill(WidgetTester tester) async {
    await tester.ensureVisible(find.byKey(const ValueKey('oauth-autofill-codex')));
    await tester.tap(find.byKey(const ValueKey('oauth-autofill-codex')));
    await tester.pump();
  }

  /// toast 2.4s 后自动滑出,快进补动画收尾,不给用例末留 pending timer。
  Future<void> drainToast(WidgetTester tester) async {
    await tester.pump(const Duration(seconds: 3));
    await tester.pumpAndSettle();
  }

  testWidgets('autofill fills both fields from codex CLI auth.json',
      (tester) async {
    final at = useTempHome();
    File(at('.codex${Platform.pathSeparator}auth.json')).writeAsStringSync(
        '{"tokens":{"refresh_token":"rt-from-file","account_id":"acc-from-file"}}');
    await pumpForm(tester);
    await selectCascade(tester, vendor: 'OpenAI', billing: '订阅', region: '全球');

    await tapAutofill(tester);

    expect(fieldText(tester, 'account-refresh-token'), 'rt-from-file');
    expect(fieldText(tester, 'account-account-id'), 'acc-from-file');
    expect(find.text('已填入 codex CLI 的登录态'), findsOneWidget);
    await drainToast(tester);
  });

  testWidgets('autofill falls back to CC Switch vault when CLI is api-key login',
      (tester) async {
    final at = useTempHome();
    // 你本机的真实形态:CLI 被 CC Switch 接管成 API Key 登录
    File(at('.codex${Platform.pathSeparator}auth.json'))
        .writeAsStringSync('{"OPENAI_API_KEY":"sk-x"}');
    File(at('.cc-switch${Platform.pathSeparator}codex_oauth_auth.json'))
        .writeAsStringSync(
            '{"default_account_id":"internal-1",'
            '"accounts":{"internal-1":{'
            '"account_id":"internal-1",'
            '"chatgpt_account_id":"chatgpt-acc-9",'
            '"refresh_token":"rt-cc"}}}');
    await pumpForm(tester);
    await selectCascade(tester, vendor: 'OpenAI', billing: '订阅', region: '全球');

    await tapAutofill(tester);

    expect(fieldText(tester, 'account-refresh-token'), 'rt-cc');
    expect(fieldText(tester, 'account-account-id'), 'chatgpt-acc-9',
        reason: '取 chatgpt_account_id 而非 CC Switch 内部 account_id');
    expect(find.text('已填入 CC Switch 的登录态'), findsOneWidget);
    await drainToast(tester);
  });

  testWidgets('autofill reports all misses and keeps fields blank',
      (tester) async {
    useTempHome(); // 空目录:两个来源都不存在
    await pumpForm(tester);
    await selectCascade(tester, vendor: 'OpenAI', billing: '订阅', region: '全球');

    await tapAutofill(tester);

    expect(fieldText(tester, 'account-refresh-token'), isEmpty);
    expect(fieldText(tester, 'account-account-id'), isEmpty);
    expect(find.textContaining('codex CLI: '), findsOneWidget);
    expect(find.textContaining('CC Switch: '), findsOneWidget);
    expect(find.textContaining('不存在'), findsNWidgets(2));
    await drainToast(tester);
  });

  testWidgets('autofill skips malformed CLI auth and reports CC Switch miss',
      (tester) async {
    final at = useTempHome();
    File(at('.codex${Platform.pathSeparator}auth.json'))
        .writeAsStringSync('{"tokens":{"refresh_token":"rt-only"}}');
    await pumpForm(tester);
    await selectCascade(tester, vendor: 'OpenAI', billing: '订阅', region: '全球');

    await tapAutofill(tester);

    expect(fieldText(tester, 'account-refresh-token'), isEmpty,
        reason: '缺 account_id 视为格式错误,两个字段都不半填');
    expect(find.textContaining('缺少 tokens.refresh_token'), findsOneWidget);
    expect(find.textContaining('CC Switch: '), findsOneWidget,
        reason: '第一个来源坏掉后继续探测第二个并一并汇报');
    await drainToast(tester);
  });

  testWidgets('parseCodexAuthJson handles both shapes and rejects garbage',
      (tester) async {
    expect(() => parseCodexAuthJson('"just a string"'),
        throwsA(isA<FormatException>()));
    expect(
        parseCodexAuthJson(
            '{"tokens":{"refresh_token":"rt","account_id":"acc","access_token":"at"}}'),
        ('rt', 'acc'),
        reason: 'codex CLI 形态:多余字段忽略,只取登录续期所需两项');
    expect(
        parseCodexAuthJson('{"default_account_id":"i1","accounts":{"i1":'
            '{"refresh_token":"rt2","chatgpt_account_id":"cg-2"}}}'),
        ('rt2', 'cg-2'),
        reason: 'CC Switch 形态:按 default_account_id 取默认账号');
    expect(() => parseCodexAuthJson('{"OPENAI_API_KEY":"sk-x"}'),
        throwsA(isA<FormatException>()),
        reason: '纯 API Key 登录没有订阅登录态,报格式错误让调用方换下一个来源');
  });
}

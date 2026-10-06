import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:flutter_svg/flutter_svg.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:msu_admin/api_client.dart';
import 'package:msu_admin/pages/providers_page.dart';
import 'package:msu_admin/theme.dart';

ApiClient fakeClient() => ApiClient(
  baseUrl: 'http://127.0.0.1:8080',
  adminKey: 'adm',
  httpClient: MockClient(
    (_) async => http.Response(
      jsonEncode({
        'providers': [
          {
            'id': 'kimi.global.subscribe.coding',
            'display_name': 'Moonshot Kimi',
            'website': 'https://www.kimi.com',
            'base_url': 'https://api.kimi.com/coding',
            'protocols': ['anthropic', 'chat_completions'],
            'auth': 'anthropic_key',
            'credential': 'api_key',
            'billing': 'subscription',
            'region': 'Global',
          },
          {
            'id': 'ark.cn.api.standard',
            'display_name': 'Volcengine Ark',
            'website': 'https://console.volcengine.com/ark',
            'base_url': 'https://ark.cn-beijing.volces.com/api/v3',
            'protocols': ['anthropic', 'chat_completions'],
            'auth': 'bearer',
            'credential': 'api_key',
            'billing': 'paygo',
            'region': 'CN',
          },
          {
            'id': 'openai.global.api.standard',
            'display_name': 'OpenAI',
            'website': 'https://openai.com',
            'base_url': 'https://api.openai.com',
            'protocols': ['chat_completions', 'responses'],
            'auth': 'bearer',
            'credential': 'api_key',
            'billing': 'paygo',
            'region': 'Global',
          },
        ],
      }),
      200,
      headers: {'content-type': 'application/json'},
    ),
  ),
);

void main() {
  testWidgets('Kiro displays subscription, protocols, credential label and provider logo',
      (tester) async {
    final client = ApiClient(
      baseUrl: 'http://127.0.0.1:8080',
      adminKey: 'adm',
      httpClient: MockClient((_) async => http.Response(jsonEncode({
        'providers': [{
          'id': 'kiro',
          'display_name': 'Kiro',
          'website': 'https://kiro.dev',
          'base_url': 'https://q.us-east-1.amazonaws.com',
          'protocols': ['anthropic', 'chat_completions'],
          'auth': 'bearer',
          'credential': 'kiro_refresh',
          'billing': 'subscription',
          'region': 'Global',
        }],
      }), 200, headers: {'content-type': 'application/json'})),
    );
    await tester.pumpWidget(MaterialApp(
      theme: buildAppTheme(),
      home: Scaffold(body: ProvidersPage(client: client, onOpenSettings: () {})),
    ));
    await tester.pumpAndSettle();
    expect(find.text('Kiro'), findsOneWidget);
    expect(find.text('kiro'), findsOneWidget);
    expect(find.text('订阅 · 全球'), findsOneWidget);
    expect(find.text('anthropic, chat_completions'), findsOneWidget);
    expect(find.text('Kiro 登录态 (Desktop / SSO)'), findsOneWidget);
    final logo = tester.widget<Image>(find.byType(Image));
    expect((logo.image as AssetImage).assetName, 'assets/providers/kiro.png');
    expect(find.text('K'), findsNothing);
    expect(tester.takeException(), isNull);
  });

  testWidgets('百炼订阅中国版显示厂商、端点与 SVG Logo', (tester) async {
    final client = ApiClient(
      baseUrl: 'http://127.0.0.1:8080',
      adminKey: 'adm',
      httpClient: MockClient(
        (_) async => http.Response(
          jsonEncode({
            'providers': [
              {
                'id': 'bailian.cn.subscribe.token-plan',
                'display_name': 'Aliyun Bailian',
                'website':
                    'https://bailian.console.aliyun.com/cn-beijing/subscription/token-plan/personal',
                'base_url':
                    'https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode',
                'protocols': ['chat_completions'],
                'auth': 'bearer',
                'credential': 'api_key',
                'billing': 'subscription',
                'region': 'CN',
                'plan': 'Token Plan',
              },
            ],
          }),
          200,
          headers: {'content-type': 'application/json'},
        ),
      ),
    );
    await tester.pumpWidget(
      MaterialApp(
        theme: buildAppTheme(),
        home: Scaffold(
          body: ProvidersPage(client: client, onOpenSettings: () {}),
        ),
      ),
    );
    await tester.pumpAndSettle();
    expect(find.text('Aliyun Bailian'), findsOneWidget);
    expect(find.text('订阅 · 中国 · Token Plan'), findsOneWidget);
    expect(find.text('服务类型'), findsOneWidget);
    expect(find.widgetWithText(SelectableText, 'Token Plan'), findsOneWidget);
    expect(find.text('bailian'), findsOneWidget);
    expect(
      find.text(
        'https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode',
      ),
      findsOneWidget,
    );
    expect(find.byType(SvgPicture), findsOneWidget);
    expect(find.text('B'), findsNothing);
    expect(tester.takeException(), isNull);
  });

  testWidgets('厂商大卡按类型分节展示:类型签+缩写标签+属性行', (tester) async {
    await tester.pumpWidget(
      MaterialApp(
        theme: buildAppTheme(),
        home: Scaffold(
          body: ProvidersPage(client: fakeClient(), onOpenSettings: () {}),
        ),
      ),
    );
    await tester.pumpAndSettle();

    // 卡头是厂商名(不带区域后缀),计费/区域由类型签承载
    expect(find.text('Moonshot Kimi'), findsOneWidget);
    expect(
      find.text('Volcengine Ark'),
      findsOneWidget,
      reason: '分组形态下区域在类型签里,厂商名不加 -CN 后缀',
    );
    expect(find.text('OpenAI'), findsOneWidget);

    // 类型签「计费模式 · 服务区域」:三种组合各一处
    expect(find.text('订阅 · 全球'), findsOneWidget, reason: 'kimi 订阅/Global');
    expect(find.text('按量计费 · 中国'), findsOneWidget, reason: 'ark 按量/CN');
    expect(find.text('按量计费 · 全球'), findsOneWidget, reason: 'openai 按量/Global');
    expect(find.text('计费模式'), findsNothing, reason: '独立属性行已被类型签取代');
    expect(find.text('服务区域'), findsNothing);

    // 缩写标签只显示厂商短名(点式 id 首段),不显示完整 id
    expect(find.text('kimi'), findsOneWidget);
    expect(find.text('ark'), findsOneWidget);
    expect(find.text('openai'), findsOneWidget);
    expect(find.textContaining('global.subscribe'), findsNothing,
        reason: '标签不再是完整点式 id');

    // 属性行按节下发(官网/请求地址每节各一份);服务类型行对无真实
    // 服务类型(Standard 占位/空)的规格隐藏
    expect(find.text('服务类型'), findsNothing);
    expect(find.widgetWithText(SelectableText, '—'), findsNothing);
    expect(find.text('官网'), findsNWidgets(3));
    expect(find.text('请求地址'), findsNWidgets(3));
    expect(find.text('额度查询'), findsNothing, reason: '额度查询由账号脚本配置决定,不是供应商的属性');
    expect(find.text('额度形态'), findsNothing);
    expect(find.text('额度重置'), findsNothing);
  });

  testWidgets('同厂商多类型并入一张卡分节,订阅在前', (tester) async {
    final client = ApiClient(
      baseUrl: 'http://127.0.0.1:8080',
      adminKey: 'adm',
      httpClient: MockClient(
        (_) async => http.Response(
          jsonEncode({
            'providers': [
              {
                'id': 'kimi.cn.api.standard',
                'display_name': 'Moonshot Kimi',
                'website': 'https://platform.moonshot.cn',
                'base_url': 'https://api.moonshot.cn/v1',
                'protocols': ['chat_completions'],
                'auth': 'bearer',
                'credential': 'api_key',
                'billing': 'paygo',
                'region': 'CN',
              },
              {
                'id': 'kimi.global.subscribe.coding',
                'display_name': 'Moonshot Kimi',
                'website': 'https://www.kimi.com',
                'base_url': 'https://api.kimi.com/coding',
                'protocols': ['anthropic', 'chat_completions'],
                'auth': 'anthropic_key',
                'credential': 'api_key',
                'billing': 'subscription',
                'region': 'Global',
              },
            ],
          }),
          200,
          headers: {'content-type': 'application/json'},
        ),
      ),
    );
    await tester.pumpWidget(
      MaterialApp(
        theme: buildAppTheme(),
        home: Scaffold(
          body: ProvidersPage(client: client, onOpenSettings: () {}),
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(
      find.text('Moonshot Kimi'),
      findsOneWidget,
      reason: '两条同厂商记录并入一卡,厂商名只出现一次',
    );
    expect(find.text('订阅 · 全球'), findsOneWidget);
    expect(find.text('按量计费 · 中国'), findsOneWidget);
    final subTop = tester.getTopLeft(find.text('订阅 · 全球')).dy;
    final paygoTop = tester.getTopLeft(find.text('按量计费 · 中国')).dy;
    expect(subTop, lessThan(paygoTop), reason: '组内订阅类型排在按量前面');
    expect(find.text('kimi'), findsNWidgets(2), reason: '两节标签都只显示厂商短名');
    expect(find.text('服务类型'), findsNothing,
        reason: '两条记录都无真实服务类型,该行隐藏');
    expect(find.widgetWithText(SelectableText, '—'), findsNothing);
    expect(tester.takeException(), isNull);
  });

  testWidgets('同厂商同计费同区域的多服务类型各成一节,类型签带类型名', (tester) async {
    final client = ApiClient(
      baseUrl: 'http://127.0.0.1:8080',
      adminKey: 'adm',
      httpClient: MockClient(
        (_) async => http.Response(
          jsonEncode({
            'providers': [
              {
                'id': 'bailian.cn.subscribe.token-plan',
                'display_name': 'Aliyun Bailian',
                'website': 'https://example.com/token-plan',
                'base_url': 'https://token-plan.example.com',
                'protocols': ['chat_completions'],
                'auth': 'bearer',
                'credential': 'api_key',
                'billing': 'subscription',
                'region': 'CN',
                'plan': 'Token Plan',
              },
              {
                'id': 'bailian.cn.subscribe.coding-plan',
                'display_name': 'Aliyun Bailian',
                'website': 'https://example.com/coding-plan',
                'base_url': 'https://coding-plan.example.com',
                'protocols': ['anthropic'],
                'auth': 'bearer',
                'credential': 'api_key',
                'billing': 'subscription',
                'region': 'CN',
                'plan': 'Coding Plan',
              },
            ],
          }),
          200,
          headers: {'content-type': 'application/json'},
        ),
      ),
    );
    await tester.pumpWidget(
      MaterialApp(
        theme: buildAppTheme(),
        home: Scaffold(
          body: ProvidersPage(client: client, onOpenSettings: () {}),
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('Aliyun Bailian'), findsOneWidget,
        reason: '两个服务类型并入一张厂商卡');
    expect(find.text('订阅 · 中国 · Coding Plan'), findsOneWidget);
    expect(find.text('订阅 · 中国 · Token Plan'), findsOneWidget);
    expect(find.text('服务类型'), findsNWidgets(2));
    expect(find.widgetWithText(SelectableText, 'Coding Plan'), findsOneWidget);
    expect(find.widgetWithText(SelectableText, 'Token Plan'), findsOneWidget);
    final codingTop =
        tester.getTopLeft(find.text('订阅 · 中国 · Coding Plan')).dy;
    final tokenTop =
        tester.getTopLeft(find.text('订阅 · 中国 · Token Plan')).dy;
    expect(codingTop, lessThan(tokenTop), reason: '组内服务类型按名排序');
  });
}

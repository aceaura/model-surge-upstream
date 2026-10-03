import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:msu_admin/api_client.dart';
import 'package:msu_admin/pages/providers_page.dart';
import 'package:msu_admin/theme.dart';

ApiClient fakeClient() => ApiClient(
      baseUrl: 'http://127.0.0.1:8080',
      adminKey: 'adm',
      httpClient: MockClient((_) async => http.Response(
          jsonEncode({
            'providers': [
              {
                'id': 'kimi',
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
                'id': 'ark',
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
                'id': 'openai',
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
          headers: {'content-type': 'application/json'})),
    );

void main() {
  testWidgets('厂商大卡按类型分节展示:类型签+缩写标签+属性行', (tester) async {
    await tester.pumpWidget(MaterialApp(
      theme: buildAppTheme(),
      home: Scaffold(
        body: ProvidersPage(client: fakeClient(), onOpenSettings: () {}),
      ),
    ));
    await tester.pumpAndSettle();

    // 卡头是厂商名(不带区域后缀),计费/区域由类型签承载
    expect(find.text('Moonshot Kimi'), findsOneWidget);
    expect(find.text('Volcengine Ark'), findsOneWidget,
        reason: '分组形态下区域在类型签里,厂商名不加 -CN 后缀');
    expect(find.text('OpenAI'), findsOneWidget);

    // 类型签「计费模式 · 服务区域」:三种组合各一处
    expect(find.text('订阅 · 全球'), findsOneWidget, reason: 'kimi 订阅/Global');
    expect(find.text('按量计费 · 中国'), findsOneWidget, reason: 'ark 按量/CN');
    expect(find.text('按量计费 · 全球'), findsOneWidget, reason: 'openai 按量/Global');
    expect(find.text('计费模式'), findsNothing, reason: '独立属性行已被类型签取代');
    expect(find.text('服务区域'), findsNothing);

    // 缩写标签就是 provider id,不拼任何区域后缀(区域由类型签承载)
    expect(find.text('kimi'), findsOneWidget);
    expect(find.text('ark'), findsOneWidget, reason: 'CN 区域也不加 -cn 后缀');
    expect(find.textContaining('-CN'), findsNothing);
    expect(find.textContaining('-cn'), findsNothing,
        reason: '分组形态下名字一律不带区域后缀');

    // 属性行按节下发(官网/请求地址每节各一份)
    expect(find.text('官网'), findsNWidgets(3));
    expect(find.text('请求地址'), findsNWidgets(3));
    expect(find.text('额度查询'), findsNothing,
        reason: '额度查询由账号脚本配置决定,不是供应商的属性');
    expect(find.text('额度形态'), findsNothing);
    expect(find.text('额度重置'), findsNothing);
  });

  testWidgets('同厂商多类型并入一张卡分节,订阅在前', (tester) async {
    final client = ApiClient(
      baseUrl: 'http://127.0.0.1:8080',
      adminKey: 'adm',
      httpClient: MockClient((_) async => http.Response(
          jsonEncode({
            'providers': [
              {
                'id': 'kimi-cn',
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
                'id': 'kimi',
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
          headers: {'content-type': 'application/json'})),
    );
    await tester.pumpWidget(MaterialApp(
      theme: buildAppTheme(),
      home: Scaffold(body: ProvidersPage(client: client, onOpenSettings: () {})),
    ));
    await tester.pumpAndSettle();

    expect(find.text('Moonshot Kimi'), findsOneWidget,
        reason: '两条同厂商记录并入一卡,厂商名只出现一次');
    expect(find.text('订阅 · 全球'), findsOneWidget);
    expect(find.text('按量计费 · 中国'), findsOneWidget);
    final subTop = tester.getTopLeft(find.text('订阅 · 全球')).dy;
    final paygoTop = tester.getTopLeft(find.text('按量计费 · 中国')).dy;
    expect(subTop, lessThan(paygoTop), reason: '组内订阅类型排在按量前面');
    expect(find.text('kimi-cn'), findsOneWidget);
  });
}

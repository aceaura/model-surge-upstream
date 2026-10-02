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
                'website': 'https://platform.moonshot.cn',
                'base_url': 'https://api.kimi.com/coding',
                'protocols': ['anthropic', 'chat_completions'],
                'auth': 'anthropic_key',
                'credential': 'api_key',
                'billing': 'subscription',
              },
              {
                'id': 'deepseek',
                'display_name': 'DeepSeek',
                'website': 'https://platform.deepseek.com',
                'base_url': 'https://api.deepseek.com',
                'protocols': ['anthropic', 'chat_completions'],
                'auth': 'bearer',
                'credential': 'api_key',
                'billing': 'paygo',
              },
            ],
          }),
          200,
          headers: {'content-type': 'application/json'})),
    );

void main() {
  testWidgets('提供商卡片按属性行展示计费模式中文名', (tester) async {
    await tester.pumpWidget(MaterialApp(
      theme: buildAppTheme(),
      home: Scaffold(
        body: ProvidersPage(client: fakeClient(), onOpenSettings: () {}),
      ),
    ));
    await tester.pumpAndSettle();

    expect(find.text('计费模式'), findsNWidgets(2));
    expect(find.text('订阅'), findsOneWidget, reason: 'kimi 是订阅制');
    expect(find.text('按量计费'), findsOneWidget, reason: 'deepseek 是按量计费');
  });
}

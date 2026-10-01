import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:msu_admin/api_client.dart';
import 'package:msu_admin/pages/proxy_page.dart';
import 'package:msu_admin/theme.dart';

ApiClient fakeClient() => ApiClient(
      baseUrl: 'http://127.0.0.1:8080',
      adminKey: 'adm',
      httpClient: MockClient((_) async => http.Response(
          jsonEncode({
            'settings': {
              'api_key': 'sk-sp-x',
              'port': 12344,
              'lan_open': false,
            },
          }),
          200,
          headers: {'content-type': 'application/json'})),
    );

Future<void> pumpPage(WidgetTester tester) async {
  tester.view.physicalSize = const Size(1400, 1000);
  tester.view.devicePixelRatio = 1.0;
  addTearDown(tester.view.reset);
  await tester.pumpWidget(MaterialApp(
    theme: buildAppTheme(),
    home: Scaffold(
      // 真机里页面嵌在设置枢纽的滚动区中,测试用滚动壳防溢出。
      body: SingleChildScrollView(child: ProxyPage(client: fakeClient())),
    ),
  ));
  await tester.pumpAndSettle();
}

void main() {
  testWidgets('复制按钮紧跟地址文末,不被顶到行尾右端', (tester) async {
    await pumpPage(tester);

    // 三协议共用一个 base URL,接入地址只剩一行。
    final texts = find.byType(SelectableText);
    final buttons = find.byTooltip('复制地址');
    expect(texts, findsOneWidget);
    expect(buttons, findsOneWidget);

    final textRect = tester.getRect(texts.first);
    final btnRect = tester.getRect(buttons.first);
    // 松散适配下地址框即文字宽度,按钮左缘应贴着文字右缘
    // (测试字体下 SelectableText 盒比字形窄约 1em,留 16 容差)。
    expect(btnRect.left - textRect.right, inInclusiveRange(-1, 16));
    // 若误用 Expanded,地址框撑满整行,按钮会落在行尾(约 1300+);
    // 地址本身不足千像素,按钮左缘应远小于行宽。
    expect(btnRect.left, lessThan(900),
        reason: '复制按钮应随地址文末,而非被推到行尾右端');
  });

  testWidgets('点复制按钮回显所抄地址', (tester) async {
    String? copied;
    TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger
        .setMockMethodCallHandler(SystemChannels.platform, (call) async {
      if (call.method == 'Clipboard.setData') {
        copied = (call.arguments as Map)['text'] as String;
      }
      return null;
    });

    await pumpPage(tester);

    await tester.tap(find.byTooltip('复制地址').first);
    await tester.pumpAndSettle();

    expect(copied, 'http://127.0.0.1:12344');
    expect(
      find.textContaining('已复制 http://127.0.0.1:12344'),
      findsOneWidget,
    );

    // 冲刷提示框的 2.4s 驻留定时器与滑出动画,避免遗留 Timer。
    await tester.pump(const Duration(seconds: 3));
    await tester.pumpAndSettle();
  });
}

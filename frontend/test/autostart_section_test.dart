import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:msu_admin/pages/autostart_section.dart';
import 'package:msu_admin/theme.dart';

Widget _wrap(Widget child) =>
    MaterialApp(theme: buildAppTheme(), home: Scaffold(body: child));

void main() {
  testWidgets('开机自启动:初始态反映读取结果,切换即写入并更新文案', (tester) async {
    var enabled = false;
    final writes = <bool>[];
    await tester.pumpWidget(_wrap(AutostartSection(
      probe: () async => enabled,
      apply: (v) async {
        writes.add(v);
        enabled = v;
      },
    )));
    await tester.pumpAndSettle();

    expect(find.text('已关闭:登录后不自动启动'), findsOneWidget);

    // 卡片默认收起,先展开再点开关
    await tester.tap(find.text('启动'));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const ValueKey('autostart-enabled')));
    await tester.pumpAndSettle();

    expect(writes, [true], reason: '切换开关应立即写入自启动状态');
    expect(find.text('已开启:登录 Windows 后自动启动'), findsOneWidget);
  });

  testWidgets('开机自启动:读取期间开关禁用,避免未加载先误触', (tester) async {
    await tester.pumpWidget(_wrap(AutostartSection(
      probe: () async => true,
      apply: (_) async {},
    )));
    // 未 pump 完成异步读取前
    expect(find.text('读取中…'), findsOneWidget);
    final sw = tester.widget<Switch>(find.byKey(const ValueKey('autostart-enabled')));
    expect(sw.onChanged, isNull);
    await tester.pumpAndSettle();
    expect(find.text('已开启:登录 Windows 后自动启动'), findsOneWidget);
  });
}

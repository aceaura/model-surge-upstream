import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:msu_admin/theme.dart';
import 'package:msu_admin/ui/top_toast.dart';

/// TopToast 组件测试:顶部中间滑出的浅色卡片提示(仿 fengshen-slicer),
/// 成功/失败共用骨架,仅图标与图标色区分;2.4s 驻留后自动滑出并移除。
Future<void> pumpHost(WidgetTester tester, {bool error = false}) async {
  await tester.pumpWidget(MaterialApp(
    theme: buildAppTheme(),
    home: Scaffold(
      body: Builder(
        builder: (context) => Center(
          child: FilledButton(
            onPressed: () =>
                TopToast.show(context, '操作完成', error: error),
            child: const Text('触发'),
          ),
        ),
      ),
    ),
  ));
}

Future<void> flushToast(WidgetTester tester) async {
  await tester.pump(const Duration(seconds: 3));
  await tester.pumpAndSettle();
}

void main() {
  testWidgets('成功提示:顶部白卡+对勾图标,驻留后自动消失', (tester) async {
    await pumpHost(tester);

    await tester.tap(find.text('触发'));
    await tester.pump(); // 插入 OverlayEntry
    await tester.pumpAndSettle(); // 滑入动画走完

    expect(find.text('操作完成'), findsOneWidget);
    expect(find.byIcon(Icons.check_circle), findsOneWidget);
    final icon = tester.widget<Icon>(find.byIcon(Icons.check_circle));
    expect(icon.color, AppTokens.light.success);

    // 位置锁:卡片贴在窗口顶部(而非底部 snackbar 区)。
    expect(tester.getTopLeft(find.text('操作完成')).dy, lessThan(120));

    // 2.4s 驻留 + 滑出后,提示从树里移除。
    await flushToast(tester);
    expect(find.text('操作完成'), findsNothing);
  });

  testWidgets('失败提示:圆形叹号+危险色', (tester) async {
    await pumpHost(tester, error: true);

    await tester.tap(find.text('触发'));
    await tester.pump();
    await tester.pumpAndSettle();

    expect(find.byIcon(Icons.error_outline), findsOneWidget);
    expect(find.byIcon(Icons.check_circle), findsNothing);
    final icon = tester.widget<Icon>(find.byIcon(Icons.error_outline));
    expect(icon.color, AppTokens.light.danger);

    await flushToast(tester);
  });
}

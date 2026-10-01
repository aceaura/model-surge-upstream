import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

import 'package:msu_admin/theme.dart';
import 'package:msu_admin/ui/confirm_dialog.dart';
import 'package:msu_admin/ui/dialog_band.dart';

void main() {
  testWidgets('危险级(默认):红带+警告三角+深红标题,取消描边钮,确认红底钮',
      (tester) async {
    await tester.pumpWidget(MaterialApp(
      theme: buildAppTheme(),
      home: const Scaffold(
        body: ConfirmDialog(title: '删除会话', message: '不可恢复。'),
      ),
    ));

    final t = buildAppTheme().extension<AppTokens>()!;

    // 头带:dangerSoft 底 + 警告三角 + dangerInk 标题,无分隔线。
    final band = tester.widget<DialogBand>(find.byType(DialogBand));
    expect(band.icon, Icons.warning_amber_rounded);
    expect(band.background, t.dangerSoft);
    expect(band.foreground, t.dangerInk);
    expect(find.byIcon(Icons.warning_amber_rounded), findsOneWidget);
    expect(find.byType(Divider), findsNothing);

    // 取消=描边钮;确认=FilledButton 且底色为主题 danger。
    expect(find.widgetWithText(OutlinedButton, '取消'), findsOneWidget);
    final confirm = find.widgetWithText(FilledButton, '删除');
    expect(confirm, findsOneWidget);
    final btn = tester.widget<FilledButton>(confirm);
    expect(btn.style?.backgroundColor?.resolve({}), t.danger);
  });

  testWidgets('警示级:橙带+圆形叹号+橙底确认钮', (tester) async {
    await tester.pumpWidget(MaterialApp(
      theme: buildAppTheme(),
      home: const Scaffold(
        body: ConfirmDialog(
          title: '清空日志',
          message: '界面上已展示的内容一并消失。',
          confirmLabel: '清空',
          severity: ConfirmSeverity.warning,
        ),
      ),
    ));

    final t = buildAppTheme().extension<AppTokens>()!;

    final band = tester.widget<DialogBand>(find.byType(DialogBand));
    expect(band.icon, Icons.error_outline);
    expect(band.background, t.warnSoft);
    expect(band.foreground, t.warnInk);

    final btn = tester.widget<FilledButton>(
        find.widgetWithText(FilledButton, '清空'));
    expect(btn.style?.backgroundColor?.resolve({}), t.warn);
  });

  testWidgets('取消回传 false,确认回传 true', (tester) async {
    bool? result;
    await tester.pumpWidget(MaterialApp(
      theme: buildAppTheme(),
      home: Scaffold(
        body: Builder(
          builder: (ctx) => TextButton(
            onPressed: () async {
              result = await showDialog<bool>(
                context: ctx,
                builder: (_) => const ConfirmDialog(
                  title: '删除会话',
                  message: '不可恢复。',
                ),
              );
            },
            child: const Text('打开'),
          ),
        ),
      ),
    ));

    await tester.tap(find.text('打开'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('取消'));
    await tester.pumpAndSettle();
    expect(result, isFalse);

    await tester.tap(find.text('打开'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('删除'));
    await tester.pumpAndSettle();
    expect(result, isTrue);
  });
}

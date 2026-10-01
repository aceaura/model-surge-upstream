import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:msu_admin/api_client.dart';
import 'package:msu_admin/pages/usage_page.dart';
import 'package:msu_admin/theme.dart';

/// 用量页页头下拉(_FilterSelect)的组件测试:
/// 菜单必须落在触发器正下方、左右缘与触发器同宽对齐(仿 CC Switch Select),
/// 不再像 PopupMenuButton 那样盖住触发器。
///
/// 接口全部打桩:页面加载只依赖 models/summary/trend/logs 四个端点,
/// 返回空集合即可把页面渲染出来。
ApiClient _stubClient() {
  final mock = MockClient((req) async {
    final path = req.url.path;
    Object body = const {};
    if (path.endsWith('/admin/models')) {
      body = {'models': const []};
    } else if (path.endsWith('/usage/trend')) {
      body = {'granularity': 'hour', 'buckets': const []};
    } else if (path.endsWith('/usage/logs')) {
      body = {'logs': const [], 'total': 0};
    } else if (path.endsWith('/usage/accounts')) {
      body = {'accounts': const []};
    } else if (path.endsWith('/usage/models')) {
      body = {'models': const []};
    } // summary 缺字段走 fromJson 的 0 默认
    return http.Response(jsonEncode(body), 200,
        headers: {'content-type': 'application/json'});
  });
  return ApiClient(
      baseUrl: 'http://stub', adminKey: 'k', httpClient: mock);
}

Future<void> _pumpUsage(WidgetTester tester) async {
  await tester.pumpWidget(MaterialApp(
    theme: buildAppTheme(),
    home: Scaffold(body: UsagePage(client: _stubClient())),
  ));
  await tester.pumpAndSettle();
}

/// 收尾卸载页面:_UsagePageState 挂着 30s 周期定时器,
/// 不 dispose 会被 flutter_test 判 "Timer still pending"。
Future<void> _unmount(WidgetTester tester) async {
  await tester.pumpWidget(const SizedBox());
  await tester.pump();
}

void main() {
  testWidgets('区间下拉展开:菜单在触发器正下方、同宽左对齐,不遮挡触发器',
      (tester) async {
    await _pumpUsage(tester);

    // 触发器即显示当前区间标签的按钮;展开前「当天」只有触发器一处。
    final trigger = find.text('当天');
    expect(trigger, findsOneWidget);
    final triggerBox =
        tester.getRect(find.ancestor(of: trigger, matching: find.byType(GestureDetector)).first);
    await tester.tap(trigger);
    await tester.pump();

    // 菜单项出现(含「当天」自身,现在应有两处:触发器 + 菜单选中项)。
    expect(find.text('近 7 天'), findsOneWidget);
    expect(find.text('近 30 天'), findsOneWidget);
    expect(find.text('当天'), findsNWidgets(2));

    // 对齐:跟随层纵向偏移 = 触发器高 34 + 间距 4;菜单外壳(跟随层内首个
    // Container,描边/投影画在它上面)与触发器同宽、左缘对齐、顶缘贴触发器
    // 底缘下 4px,触发器保持可见不被遮挡。注意不能取 Material:Container 的
    // 描边会把子级内缩 1px,外壳矩形才是可视边缘。
    final follower = find.byType(CompositedTransformFollower);
    expect(tester.widget<CompositedTransformFollower>(follower).offset,
        const Offset(0, 38));
    final menuRect = tester.getRect(find
        .descendant(of: follower, matching: find.byType(Container))
        .first);
    expect(menuRect.left, moreOrLessEquals(triggerBox.left, epsilon: 0.5));
    expect(menuRect.top, moreOrLessEquals(triggerBox.bottom + 4, epsilon: 0.5));
    expect(menuRect.width, moreOrLessEquals(triggerBox.width, epsilon: 0.5));

    await _unmount(tester);
  });

  testWidgets('选中子项:菜单收起、触发器标签换成新区间', (tester) async {
    await _pumpUsage(tester);

    await tester.tap(find.text('当天'));
    await tester.pump();
    await tester.tap(find.text('近 7 天'));
    await tester.pumpAndSettle();

    // 菜单收起后「近 7 天」只剩触发器一处,「近 30 天」随菜单消失。
    expect(find.text('近 7 天'), findsOneWidget);
    expect(find.text('近 30 天'), findsNothing);

    await _unmount(tester);
  });

  testWidgets('点菜单外部:菜单收起、选择不变', (tester) async {
    await _pumpUsage(tester);

    await tester.tap(find.text('当天'));
    await tester.pump();
    expect(find.text('近 7 天'), findsOneWidget);

    // 点在菜单外的页面空白处(透明屏障),菜单收起且触发器仍是「当天」。
    await tester.tapAt(const Offset(20, 500));
    await tester.pump();
    expect(find.text('近 7 天'), findsNothing);
    expect(find.text('当天'), findsOneWidget);

    await _unmount(tester);
  });
}

import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter/gestures.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:msu_admin/api_client.dart';
import 'package:msu_admin/pages/all_models_page.dart' show SearchSeed;
import 'package:msu_admin/pages/usage_page.dart';
import 'package:msu_admin/theme.dart';

/// 用量页页头下拉(_FilterSelect)的组件测试:
/// 菜单必须落在触发器正下方、左右缘与触发器同宽对齐(仿 CC Switch Select),
/// 不再像 PopupMenuButton 那样盖住触发器。
///
/// 接口全部打桩:页面加载只依赖 models/summary/trend/logs 四个端点,
/// 返回空集合即可把页面渲染出来。
ApiClient _stubClient({
  void Function(Uri uri)? onRequest,
  List<Map<String, Object>> buckets = const [],
  List<Map<String, Object>> logs = const [],
  List<Map<String, Object>> groups = const [],
}) {
  final mock = MockClient((req) async {
    onRequest?.call(req.url);
    final path = req.url.path;
    Object body = const {};
    if (path.endsWith('/admin/models')) {
      body = {'models': const []};
    } else if (path.endsWith('/usage/trend')) {
      body = {'granularity': 'hour', 'buckets': buckets};
    } else if (path.endsWith('/usage/logs')) {
      body = {'logs': logs, 'total': logs.length};
    } else if (path.endsWith('/usage/accounts')) {
      body = {'accounts': groups};
    } else if (path.endsWith('/usage/models')) {
      body = {'models': groups};
    } // summary 缺字段走 fromJson 的 0 默认
    return http.Response(jsonEncode(body), 200,
        headers: {'content-type': 'application/json'});
  });
  return ApiClient(
      baseUrl: 'http://stub', adminKey: 'k', httpClient: mock);
}

Future<void> _pumpUsage(WidgetTester tester,
    {ApiClient? client, ValueNotifier<SearchSeed?>? modelSeed}) async {
  await tester.pumpWidget(MaterialApp(
    theme: buildAppTheme(),
    home: Scaffold(
        body: UsagePage(client: client ?? _stubClient(), modelSeed: modelSeed)),
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
  testWidgets('卡片与趋势图例依次展示输入、缓存创建、缓存命中、输出',
      (tester) async {
    tester.view.physicalSize = const Size(1600, 1200);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);
    await _pumpUsage(tester);

    final cards = [
      find.text('新增输入'),
      find.text('缓存创建').first,
      find.text('缓存命中').first,
      find.text('输出').first,
    ];
    final legends = [
      find.text('输入').first,
      find.text('缓存创建').at(1),
      find.text('缓存命中').at(1),
      find.text('输出').at(1),
    ];
    for (final items in [cards, legends]) {
      for (var i = 1; i < items.length; i++) {
        expect(tester.getTopLeft(items[i]).dx,
            greaterThan(tester.getTopLeft(items[i - 1]).dx));
        expect(tester.getTopLeft(items[i]).dy,
            tester.getTopLeft(items[0]).dy);
      }
    }

    await _unmount(tester);
  });

  testWidgets('请求日志及账号、模型统计的标题和数据列保持四桶顺序',
      (tester) async {
    tester.view.physicalSize = const Size(1600, 1200);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);
    await _pumpUsage(tester, client: _stubClient(
      logs: const [
        {
          'created_at': '2026-10-03T12:00:00Z',
          'source': 'proxy',
          'account': 'log-account',
          'model_id': 'log-model',
          'input_tokens': 111,
          'cache_write_tokens': 222,
          'cache_read_tokens': 333,
          'output_tokens': 444,
          'status_code': 200,
        },
      ],
      groups: const [
        {
          'key': 'group-key',
          'requests': 7,
          'input_tokens': 555,
          'cache_write_tokens': 666,
          'cache_read_tokens': 777,
          'output_tokens': 888,
          'cache_hit_rate': 0.25,
        },
      ],
    ));

    for (final tab in ['请求日志', '账号统计', '模型统计']) {
      await tester.tap(find.text(tab));
      await tester.pumpAndSettle();
      final header = find.ancestor(
        of: find.text(tab == '请求日志' ? '时间' : (tab == '账号统计' ? '账号' : '模型')),
        matching: find.byType(Row),
      ).first;
      final data = find.ancestor(
        of: find.text(tab == '请求日志' ? 'log-account' : 'group-key'),
        matching: find.byType(Row),
      ).first;
      final headers = tester.widgetList<Text>(
          find.descendant(of: header, matching: find.byType(Text)))
          .map((text) => text.data).toList();
      final values = tester.widgetList<Text>(
          find.descendant(of: data, matching: find.byType(Text)))
          .map((text) => text.data).toList();
      if (tab == '请求日志') {
        expect(headers, ['时间', '来源', '账号', '模型', '输入', '缓存创建', '缓存命中', '输出', '用时', '状态']);
        expect(values.sublist(4, 8), ['111', '222', '333', '444']);
      } else {
        expect(headers, [tab == '账号统计' ? '账号' : '模型', '请求', '新增输入', '缓存创建', '缓存命中', '输出', '命中率']);
        expect(values, ['group-key', '7', '555', '666', '777', '888', '25.0%']);
      }
      final start = tab == '请求日志' ? 4 : 2;
      for (var i = start; i < start + 4; i++) {
        expect(tester.getTopLeft(find.descendant(
            of: header, matching: find.text(headers[i]!))).dx,
            tester.getTopLeft(find.descendant(
                of: data, matching: find.text(values[i]!))).dx);
      }
    }

    await _unmount(tester);
  });

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

  testWidgets('模型种子到达:过滤器锁定该模型,数据请求带 model 参数',
      (tester) async {
    final seen = <Uri>[];
    final seed = ValueNotifier<SearchSeed?>(null);
    addTearDown(seed.dispose);
    await _pumpUsage(tester,
        client: _stubClient(onRequest: seen.add), modelSeed: seed);

    seed.value = const SearchSeed('ds-1/v4');
    await tester.pumpAndSettle();

    // 过滤器触发器显示模型标识(模型不在选项清单也会被补入)。
    expect(find.text('ds-1/v4'), findsOneWidget);
    // 摘要/趋势/日志三类请求都带 model 过滤参数。
    final filtered = seen
        .where((u) =>
            u.queryParameters['model'] == 'ds-1/v4' &&
            u.path.startsWith('/admin/usage/'))
        .map((u) => u.path)
        .toSet();
    expect(filtered, containsAll(['/admin/usage/summary', '/admin/usage/trend', '/admin/usage/logs']));

    await _unmount(tester);
  });

  testWidgets('趋势图鼠标悬停:显示最近时间桶的四项数据浮动面板,移出隐藏',
      (tester) async {
    final buckets = <Map<String, Object>>[
      {
        'bucket': '2026-10-03T12:00:00Z',
        'input_tokens': 100, 'output_tokens': 20,
        'cache_write_tokens': 0, 'cache_read_tokens': 0,
      },
      {
        'bucket': '2026-10-03T13:00:00Z',
        'input_tokens': 400, 'output_tokens': 40,
        'cache_write_tokens': 5, 'cache_read_tokens': 300,
      },
      {
        'bucket': '2026-10-03T14:00:00Z',
        'input_tokens': 700, 'output_tokens': 60,
        'cache_write_tokens': 9, 'cache_read_tokens': 500,
      },
    ];
    await _pumpUsage(tester, client: _stubClient(buckets: buckets));

    // 趋势卡在页面中下部,放大测试视口确保图表在可视区内可被悬停命中。
    tester.view.physicalSize = const Size(1600, 1200);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);
    await tester.pump();

    expect(find.byKey(const ValueKey('usage-trend-tooltip')), findsNothing);
    final chart = tester.getRect(find.byKey(const ValueKey('usage-trend-chart')));

    // 三个桶按等间距分布,悬停在右端桶(14:00)附近(略入绘图区,右缘为开区间)。
    final hover = Offset(chart.right - 12, chart.top + 110);
    final gesture = await tester.createGesture(kind: PointerDeviceKind.mouse);
    addTearDown(gesture.removePointer);
    await gesture.addPointer();
    await gesture.moveTo(hover);
    await tester.pump();

    expect(find.byKey(const ValueKey('usage-trend-tooltip')), findsOneWidget);
    expect(find.text('输入: 700'), findsOneWidget);
    expect(find.text('缓存创建: 9'), findsOneWidget);
    expect(find.text('缓存命中: 500'), findsOneWidget);
    expect(find.text('输出: 60'), findsOneWidget);
    final tooltipTexts = tester.widgetList<Text>(find.descendant(
      of: find.byKey(const ValueKey('usage-trend-tooltip')),
      matching: find.byType(Text),
    )).map((text) => text.data).toList();
    expect(tooltipTexts.skip(1).toList(),
        ['输入: 700', '缓存创建: 9', '缓存命中: 500', '输出: 60']);
    for (final pair in [
      ('输入: 700', '缓存创建: 9'),
      ('缓存创建: 9', '缓存命中: 500'),
      ('缓存命中: 500', '输出: 60'),
    ]) {
      expect(tester.getTopLeft(find.text(pair.$2)).dy,
          greaterThan(tester.getTopLeft(find.text(pair.$1)).dy));
    }

    await gesture.moveTo(Offset.zero);
    await tester.pump();
    expect(find.byKey(const ValueKey('usage-trend-tooltip')), findsNothing);

    await _unmount(tester);
  });
}

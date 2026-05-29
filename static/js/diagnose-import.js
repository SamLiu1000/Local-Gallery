/* ============================================================
   diagnose-import.js — 导入文件夹全链路诊断脚本

   用法:
     1. 在 index.html 中加载此脚本（在 app.js 之前）
     2. 打开浏览器 DevTools 控制台
     3. 导入一个文件夹，观察控制台日志

   输出: 每个步骤的时间戳、状态、关键数据，帮助定位
          "计数从正确变为0" 和 "点击文件夹无反应" 的根因
   ============================================================ */

(function () {
  'use strict';

  const TAG = '[DIAG]';
  let stepId = 0;
  let importStartTime = 0;

  function now() {
    return Date.now();
  }

  function elapsed() {
    return importStartTime ? '+' + (now() - importStartTime) + 'ms' : '+0ms';
  }

  function timestamp() {
    return new Date().toISOString().slice(11, 23);
  }

  function step(label, detail) {
    stepId++;
    const prefix = TAG + ' [' + String(stepId).padStart(3, '0') + '] ' + elapsed().padEnd(8);
    const line = prefix + ' ' + label;
    if (detail !== undefined) {
      console.groupCollapsed(line);
      if (typeof detail === 'object') {
        console.log(JSON.stringify(detail, null, 2));
      } else {
        console.log(detail);
      }
      console.groupEnd();
    } else {
      console.log(line);
    }
  }

  function stepError(label, detail) {
    stepId++;
    console.error(TAG + ' [' + String(stepId).padStart(3, '0') + '] ' + elapsed().padEnd(8) + ' ❌ ' + label, detail || '');
  }

  // ==================== 后端状态快照 ====================

  async function snapshotBackendState(label) {
    try {
      const app = window['go'] && window['go']['main'] && window['go']['main']['App'];
      if (!app) return;

      let images = 0, folderIndex = 0, folderCount = 0, registeredRoots = 0;
      try {
        const state = await app.DebugAppState();
        images = state.totalImages || 0;
        registeredRoots = state.totalRoots || 0;
        if (state.roots && state.roots.length > 0) {
          folderCount = state.roots[0].folderCount || 0;
          folderIndex = state.roots[0].folderIndexCount || 0;
        }
      } catch (e) { /* DebugAppState 可能很慢，抽取核心数据 */ }

      step(label, {
        images: images,
        folderIndex: folderIndex,
        folderCount: folderCount,
        registeredRoots: registeredRoots
      });
    } catch (e) {
      stepError(label + ' (snapshot失败)', e.message);
    }
  }

  // ==================== 前端状态快照 ====================

  function snapshotFrontendState(label) {
    const gallery = window.Gallery || {};
    const sidebar = window.Sidebar || {};

    step(label, {
      currentFolderFilter: gallery.currentFolderFilter || null,
      isFilteringActive: gallery.isFilteringActive || false,
      imagesCount: (gallery.images && gallery.images.length) || 0,
      folderLoadTotal: gallery.folderLoadTotal || 0,
      folderRootsCount: (sidebar.folderRoots && sidebar.folderRoots.length) || 0,
      sidebarFolderTreeLoaded: sidebar.folderTreeLoaded || false,
    });
  }

  // ==================== Backend RPC 拦截 ====================

  function wrapRPC(methodName, desc, extraLog) {
    const app = window['go'] && window['go']['main'] && window['go']['main']['App'];
    if (!app) return;

    const original = app[methodName];
    if (!original || original.__diagnoseWrapped) return;

    app[methodName] = async function (...args) {
      step('RPC → ' + desc + ' 调用', extraLog ? extraLog(args) : { args });
      const t0 = now();
      let result;
      try {
        result = await original.apply(this, args);
      } catch (e) {
        stepError('RPC → ' + desc + ' 失败 (' + (now() - t0) + 'ms)', e.message);
        throw e;
      }
      step('RPC ← ' + desc + ' 返回 (' + (now() - t0) + 'ms)',
        extraLog ? extraLog(args, result) : { result });
      return result;
    };
    app[methodName].__diagnoseWrapped = true;
  }

  function wrapRPCAfterLoad() {
    wrapRPC('ScanFolderQuick', 'ScanFolderQuick',
      (args) => ({ path: args[0], folderType: args[1], quick: args[2] }));

    wrapRPC('GetFolders', 'GetFolders');

    wrapRPC('GetImages', 'GetImages',
      (args) => ({ folder: args[0], offset: args[1], limit: args[2], sort: args[3] }));
    // 原版 GetImages 签名: GetImages(folder, offset, limit, sortOrder)
    // 但是前端 WailsBridge 调用时传的是一个对象: { folder, offset, limit, sortOrder }
    // 所以我们还需要拦截 WailsBridge.getImages
  }

  // ==================== 前端函数拦截 ====================

  function wrapFrontendAfterInit() {
    // --- Sidebar ---
    const Sidebar = window.Sidebar || {};
    const Gallery = window.Gallery || {};

    // refreshFolderTree
    if (Sidebar._origRefreshFolderTree === undefined) {
      const orig = Sidebar.refreshFolderTree;
      if (orig) {
        Sidebar._origRefreshFolderTree = orig;
        Sidebar.refreshFolderTree = async function () {
          const t0 = now();
          const folderRootsBefore = (Sidebar.folderRoots && Sidebar.folderRoots.length) || 0;
          step('Sidebar.refreshFolderTree 调用', { folderRootsBefore });
          try {
            const result = await orig.apply(this, arguments);
            const folderRootsAfter = (Sidebar.folderRoots && Sidebar.folderRoots.length) || 0;
            const firstCount = Sidebar.folderRoots && Sidebar.folderRoots[0]
              ? Sidebar.folderRoots[0].imageCount : 'N/A';
            step('Sidebar.refreshFolderTree 完成 (' + (now() - t0) + 'ms)', {
              folderRootsBefore: folderRootsBefore,
              folderRootsAfter: folderRootsAfter,
              firstFolderCount: firstCount,
              firstFolderPath: Sidebar.folderRoots && Sidebar.folderRoots[0]
                ? Sidebar.folderRoots[0].path : 'N/A'
            });
            return result;
          } catch (e) {
            stepError('Sidebar.refreshFolderTree 失败', e.message);
            throw e;
          }
        };
      }
    }

    // pollScanProgress
    if (Sidebar._origPollScanProgress === undefined) {
      const orig = Sidebar.pollScanProgress;
      if (orig) {
        Sidebar._origPollScanProgress = orig;
        Sidebar.pollScanProgress = function (folderPath, folderName) {
          step('Sidebar.pollScanProgress 开始', { folderPath, folderName });
          importStartTime = now();
          return orig.apply(this, arguments);
        };
      }
    }

    // --- Gallery ---

    // filterByFolder
    if (Gallery._origFilterByFolder === undefined) {
      const orig = Gallery.filterByFolder;
      if (orig) {
        Gallery._origFilterByFolder = orig;
        Gallery.filterByFolder = async function (folderPath, folderName, options) {
          const t0 = now();
          step('Gallery.filterByFolder 调用', {
            folderPath, folderName,
            forceRefresh: options && options.forceRefresh
          });
          snapshotFrontendState('  前状态');
          let result;
          try {
            result = await orig.apply(this, arguments);
          } catch (e) {
            stepError('Gallery.filterByFolder 失败 (' + (now() - t0) + 'ms)', e.message);
            throw e;
          }
          snapshotFrontendState('  后状态 (' + (now() - t0) + 'ms)');
          step('Gallery.filterByFolder 完成', { imagesAfter: (Gallery.images && Gallery.images.length) || 0 });
          return result;
        };
      }
    }

    // loadImagesFromServer
    if (Gallery._origLoadImagesFromServer === undefined) {
      const orig = Gallery.loadImagesFromServer;
      if (orig) {
        Gallery._origLoadImagesFromServer = orig;
        Gallery.loadImagesFromServer = async function (folderPath, offset, limit) {
          const t0 = now();
          step('Gallery.loadImagesFromServer 调用', { folderPath, offset, limit });
          let result;
          try {
            result = await orig.apply(this, arguments);
          } catch (e) {
            stepError('Gallery.loadImagesFromServer 失败 (' + (now() - t0) + 'ms)', e.message);
            throw e;
          }
          step('Gallery.loadImagesFromServer 返回 (' + (now() - t0) + 'ms)', {
            images: result.images ? result.images.length : 0,
            total: result.total || 0
          });
          return result;
        };
      }
    }
  }

  // ==================== Wails Event 拦截 ====================

  function wrapWailsEvents() {
    const app = window['go'] && window['go']['main'] && window['go']['main']['App'];
    if (!app) return;

    const origEventsOn = app.EventsOn;
    if (origEventsOn && !origEventsOn.__diagnoseWrapped) {
      app.EventsOn = function (eventName, callback) {
        const wrappedCallback = function (data) {
          step('Event → ' + eventName, data || {});
          callback(data);
        };
        return origEventsOn.call(app, eventName, wrappedCallback);
      };
      app.EventsOn.__diagnoseWrapped = true;
    }

    // 监听所有已知事件
    try {
      if (window.runtime && window.runtime.EventsOn) {
        const eventsToLog = ['scan:complete', 'scan:batch', 'scan:start', 'folder:added', 'folder:importing'];
        eventsToLog.forEach(evt => {
          window.runtime.EventsOn(evt, (data) => {
            step('Event(runtime) → ' + evt, data || {});
          });
        });
      }
    } catch (e) {
      console.warn(TAG + ' 无法注册 runtime 事件:', e.message);
    }
  }

  // ==================== 初始化 ====================

  function init() {
    console.log('%c' + TAG + ' ========== 导入全链路诊断已激活 ==========',
      'color: #fff; background: #e91e63; padding: 4px 8px; font-weight: bold');
    console.log(TAG + ' 时间戳格式: [步骤号] +相对时间  事件描述');
    console.log(TAG + ' 导入文件夹后，观察从文件夹选择 → 计数出现 → 可能归零的完整过程');
    console.log(TAG + '');

    wrapWailsEvents();

    // 延迟包装 RPC，等待 Wails 运行时加载
    setTimeout(wrapRPCAfterLoad, 100);

    // 延迟包装前端函数，等待模块初始化
    setTimeout(wrapFrontendAfterInit, 2000);
  }

  // ==================== 手动诊断命令 ====================

  // 暴露到全局，可在控制台调用
  window.diag = {
    /** 打印当前完整状态 */
    status: async function () {
      snapshotFrontendState('--- 前端状态 ---');
      await snapshotBackendState('--- 后端状态 ---');

      // 后端详细诊断
      try {
        const app = window['go'] && window['go']['main'] && window['go']['main']['App'];
        if (app) {
          const state = await app.DebugAppState();
          console.log(TAG + ' 后端详细状态:');
          console.log(JSON.stringify(state, null, 2));
        }
      } catch (e) {
        console.warn(TAG + ' DebugAppState 失败:', e.message);
      }
    },

    /** 对指定文件夹做分页诊断 */
    pagination: async function (folderPath) {
      try {
        const app = window['go'] && window['go']['main'] && window['go']['main']['App'];
        if (app) {
          const result = await app.DebugPagination(folderPath);
          console.log(TAG + ' 分页诊断:');
          console.log(JSON.stringify(result, null, 2));
        }
      } catch (e) {
        console.warn(TAG + ' DebugPagination 失败:', e.message);
      }
    },

    /** 对指定根目录做磁盘 vs 扫描对比 */
    scanRoot: async function (rootPath) {
      try {
        const app = window['go'] && window['go']['main'] && window['go']['main']['App'];
        if (app) {
          const result = await app.DebugScanRoot(rootPath);
          console.log(TAG + ' 扫描对比:');
          console.log(JSON.stringify(result, null, 2));
        }
      } catch (e) {
        console.warn(TAG + ' DebugScanRoot 失败:', e.message);
      }
    },

    /** 开始一次带诊断的导入 */
    importFolder: async function () {
      importStartTime = now();
      stepId = 0;
      console.log('%c' + TAG + ' ========== 新导入会话 ==========',
        'color: #fff; background: #4caf50; padding: 4px 8px');
      await snapshotBackendState('导入前-后端状态');
      snapshotFrontendState('导入前-前端状态');

      // 触发导入
      if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
        const folderPath = await WailsBridge.selectFolder();
        if (!folderPath) {
          step('用户取消选择');
          return;
        }
        step('用户选择文件夹', { folderPath });

        if (typeof Sidebar !== 'undefined' && typeof Sidebar.openSystemFolderPicker === 'function') {
          // 手动触发导入流程（绕开 Sidebar.openSystemFolderPicker 中的用户交互）
          step('开始手动导入流程');
          const folderName = folderPath.split(/[\\/]/).pop() || '未命名';

          if (typeof Gallery !== 'undefined' && Gallery.addImportedRoot) {
            Gallery.addImportedRoot({
              rootId: folderPath, path: folderPath, name: folderName, displayName: folderName
            });
          }

          const result = await WailsBridge.scanFolder(folderPath, 'mixed');
          step('ScanFolder 返回', result);

          if (result && result.success) {
            // 手动调用 pollScanProgress
            if (Sidebar._origPollScanProgress) {
              Sidebar._origPollScanProgress(folderPath, folderName);
            } else if (typeof Sidebar.pollScanProgress === 'function') {
              Sidebar.pollScanProgress(folderPath, folderName);
            }
          }
        } else {
          step('请通过正常 UI 导入，诊断日志会自动记录');
        }
      } else {
        step('请在 Wails 环境下运行此诊断');
      }
    }
  };

  // 页面加载完成后初始化
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();

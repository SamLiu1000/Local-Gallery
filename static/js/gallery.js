/* ============================================================
   gallery.js - 高性能图片画廊（v5 - 按需加载 + 进度条）
     从用户选择的文件夹直接加载图片，支持子文件夹递归导入、虚拟滚动、懒加载、网格/瀑布流布局
     新增：渐进式渲染、文件夹加载进度条、按需加载子文件夹
     ============================================================ */

const Gallery = (() => {
    const t = (typeof I18n !== 'undefined' ? I18n.t : (s) => s);

    // DOM
    let galleryScroll, galleryGrid, loadingIndicator;
    let layoutBtns, thumbnailSlider, thumbnailSizeValue, sortSelect;
    let imageCountEl, btnShowPromptCount, btnInertiaToggle;

    // 安全调用 App.showToast
    function showToast(message, type) {
        // Translate known toast keys; pass through raw strings as-is
        const translated = typeof I18n !== 'undefined' ? I18n.t(message) : message;
        if (typeof App !== 'undefined' && App.showToast) {
            App.showToast(translated, type);
        }
    }

    // 状态
    let images = [];              // 所有图片: { id, path, name, size, lastModified, metadata, url, file, rootPath, rootId, folder, relativePath, thumbnailUrl, displayName }
    let imageMap = new Map();     // id → image object，用于 O(1) 查删（增量刷新）
    let filteredImages = [];      // 过滤后的图片（当前显示的）
    let selectedImages = new Set();
    let currentLayout = 'grid';
    let thumbnailSize = 220;
    let sortOrder = 'date-desc';
    let showPromptCount = false;    // 是否显示提示词版本数量
    let promptCountMap = {};        // imagePath → count
    let currentFolderFilter = null; // 当前文件夹过滤路径
    let currentTagFilter = null;     // 当前标签过滤 ID（null 表示不在标签视图）
    const DOM_KEEP = 250;           // IntersectionObserver 预加载前后各覆盖的图片张数
    let currentFavoriteFilter = false; // 当前是否在收藏视图
    let isFilteringActive = false;
    let folderAbortController = null; // 问题一：文件夹切换中断控制器
    let folderLoadTotal = 0;          // 当前文件夹服务器的图片总数
    let folderLoadOffset = 0;         // 已加载偏移量
    const FOLDER_LOOKAHEAD = 250;    // 视窗后方保持预加载的图片张数
    let isLoadingMoreFolder = false;  // 防止重复触发增量加载
    let folderCacheMeta = {};         // { [normalizedFolderPath]: { total: number } }  切换文件夹时优先使用缓存，避免重复请求后端
    let importedRoots = [];         // [{ rootId, path, name, handleName, displayName }]
    let directoryHandles = [];      // [{ id, name, handle }] 用于 File System Access API 持久化
    let isEditMode = false;         // 编辑模式：单击选择，双击预览
    let onEditModeChange = null;    // 编辑模式变化回调

    // 批量反推状态
    let batchQueue = [];
    let batchRunning = false;
    let batchPaused = false;
    let batchStopped = false;
    let batchCompleted = 0;
    let batchFailed = 0;
    let batchTotal = 0;
    let batchResolve = null; // Promise resolve for pause wait

    const IMAGE_EXTENSIONS = new Set(['.png', '.jpg', '.jpeg', '.webp', '.bmp', '.gif', '.avif', '.svg']);
    const VIDEO_EXTENSIONS = new Set(['.mp4', '.webm', '.mkv']);
    window.tagDropdownColumnMode = 1; // 标签下拉菜单列数：1=单列（默认），2=双列

    // 搜索模式
    let isSearchMode = false;       // 是否处于搜索结果展示模式
    let searchResults = [];         // 搜索结果图片数组
    let searchTotalCount = 0;       // 搜索结果总数
    let searchQuery = '';           // 当前搜索关键词

    // ===== P3: 动态批量大小 =====
    /**
     * 根据设备性能动态调整并发解析的批量大小
     * - 移动设备：5
     * - 高端桌面（8+ 核心）：20
     * - 标准桌面：15
     * - 低端桌面：10
     * @returns {number} 最优批量大小
     */
    function getOptimalBatchSize() {
        const cores = navigator.hardwareConcurrency || 4;
        const isMobile = /Mobi|Android|iPhone|iPad/i.test(navigator.userAgent);
        if (isMobile) return 5;
        if (cores >= 8) return 20;
        if (cores >= 6) return 15;
        return 10;
    }

    // 虚拟滚动
    let scrollTop = 0;
    let containerHeight = 0;
    let renderedRange = { start: 0, end: 0 };
    let _lastRenderScrollTop = -1;    // 滞回：上次渲染时的 scrollTop，防止行边界振荡
    const OVERSCAN = 25;

    // ★ 滚动状态（模块级，供 IntersectionObserver 和 scroll 防抖共享）
    let _isScrolling = false;

    // ★ 惯性滚动引擎
    let _inertiaEnabled = false;         // 用户是否开启惯性滚动（默认关闭）
    let _inertiaV = 0;                   // 当前速度

    // ★ 拖拽滚动状态
    let _dragScroll = { active: false, pointerId: -1, startY: 0, startScroll: 0, lastY: 0, lastTime: 0, velocity: 0, rafId: 0 };
    let _inertiaId = null;               // requestAnimationFrame ID
    let _inertiaScrolling = false;       // 一次性令牌：保护本次 scrollBy 不被取消
    let _lastWheelTime = 0;              // 最后一次滚轮事件时间戳
    let _lastFrameTime = performance.now(); // 上一帧时间
    let ACCELERATION = 0.8;            // 加速度（1.0 ≈ 跟手，>1 加速感）
    let FRICTION_ACTIVE = 0.9;         // 滚动中摩擦
    let FRICTION_IDLE = 0.5;           // 松手后摩擦
    let MAX_VELOCITY = 75;             // 最大速度限制
    let MIN_VELOCITY = 0.5;            // 停止阈值
    let RELEASE_DELAY = 80;            // 滚轮停止 N ms 后切换为松手摩擦

    // 固定行高瀑布流布局缓存
    let masonryLayout = [];          // [{imgIndex, row, x, y, w, h}]
    let masonryTotalHeight = 0;      // 容器总高度 px
    let masonryRowHeight = 0;        // 计算时的行高（变化时重新计算）
    let masonryContainerWidth = 0;   // 计算时的容器宽度（resize 时重新计算）
    let masonryLayoutVersion = 0;    // 递增以触发重渲染
    let masonryFirstId = '';         // 布局中第一张图片的 id（检测数据替换）

    // 竖版瀑布流（Pinterest 风格）布局缓存：固定列宽，不定高度
    let pinterestLayout = [];        // [{imgIndex, col, x, y, w, h}]
    let pinterestTotalHeight = 0;    // 容器总高度 px
    let pinterestColCount = 0;       // 计算时的列数
    let pinterestColWidth = 0;       // 计算时的列宽
    let pinterestContainerWidth = 0; // 计算时的容器宽度
    let pinterestLayoutVersion = 0;  // 递增以触发重渲染
    let pinterestFirstId = '';       // 布局中第一张图片的 id（检测数据替换）

    // 列表模式缓存：每行固定高度（thumbnailSize），绝对定位
    let listLayoutVersion = 0;       // 递增以触发重渲染
    let listFirstId = '';            // 布局中第一张图片的 id
    let listTotalHeight = 0;         // 容器总高度
    let listRowHeight = 0;           // 计算时的行高
    let listContainerWidth = 0;      // 计算时的容器宽度

    // 懒加载
    let intersectionObserver = null;

    // 回调
    let onImageClick = null;
    let onSelectionChange = null;
    let onFolderTreeChange = null; // 文件夹树变化回调（重命名后通知侧边栏刷新）
    let activeImagePath = null;    // 当前点中的图片路径（用于高亮光晕）

    // ===== 渐进式渲染 & 进度条 =====
    let progressiveRenderTimer = null;
    let progressiveRenderCancel = false;

    let isProgressiveRendering = false;

    // ==================== 初始化 ====================

    async function init(callbacks) {
        galleryScroll = document.getElementById('galleryScroll');
        galleryGrid = document.getElementById('galleryGrid');
        loadingIndicator = document.getElementById('loadingIndicator');
        layoutBtns = document.querySelectorAll('.layout-option');
        const layoutDropdownBtn = document.getElementById('layoutDropdownBtn');
        const layoutDropdownIcon = document.getElementById('layoutDropdownIcon');
        const layoutDropdownMenu = document.getElementById('layoutDropdownMenu');
        thumbnailSlider = document.getElementById('thumbnailSize');
        thumbnailSizeValue = document.getElementById('thumbnailSizeValue');
        sortSelect = document.getElementById('sortOrder');
        imageCountEl = document.getElementById('imageCount');
        btnShowPromptCount = document.getElementById('btnShowPromptCount');
        btnInertiaToggle = document.getElementById('btnInertiaToggle');

        // 从服务器同步视图设置（服务器优先）
        try {
            await refreshThumbGen();
            if (typeof Storage !== 'undefined' && Storage.getSetting) {
                const savedThumbSize = await Storage.getSetting('thumbnailSize', null);
                if (savedThumbSize) thumbnailSize = parseInt(savedThumbSize);
                const savedSort = await Storage.getSetting('sortOrder', null);
                if (savedSort) sortOrder = savedSort;
                const savedLayout = await Storage.getSetting('galleryLayout', null);
                if (savedLayout) currentLayout = savedLayout;
                const savedPromptCount = await Storage.getSetting('showPromptCount', null);
                if (savedPromptCount !== null) {
                    showPromptCount = savedPromptCount;
                }
                const savedInertia = await Storage.getSetting('inertiaEnabled', null);
                if (savedInertia !== null) {
                    _inertiaEnabled = savedInertia;
                }
                // 恢复惯性滚动参数
                const savedAccel = await Storage.getSetting('inertiaAccel', null);
                if (savedAccel !== null) ACCELERATION = savedAccel;
                const savedFricActive = await Storage.getSetting('inertiaFricActive', null);
                if (savedFricActive !== null) FRICTION_ACTIVE = savedFricActive;
                const savedFricIdle = await Storage.getSetting('inertiaFricIdle', null);
                if (savedFricIdle !== null) FRICTION_IDLE = savedFricIdle;
                const savedMaxVel = await Storage.getSetting('inertiaMaxVel', null);
                if (savedMaxVel !== null) MAX_VELOCITY = savedMaxVel;
                const savedMinVel = await Storage.getSetting('inertiaMinVel', null);
                if (savedMinVel !== null) MIN_VELOCITY = savedMinVel;
                const savedReleaseDelay = await Storage.getSetting('inertiaReleaseDelay', null);
                if (savedReleaseDelay !== null) RELEASE_DELAY = savedReleaseDelay;
            }
        } catch (e) { /* 静默 */ }

        // 应用恢复的缩略图大小到 UI
        if (thumbnailSlider) thumbnailSlider.value = thumbnailSize;
        if (thumbnailSizeValue) thumbnailSizeValue.textContent = thumbnailSize + 'px';
        // 应用恢复的排序方式到 UI
        if (sortSelect) sortSelect.value = sortOrder;
        // 应用恢复的布局模式到 UI（更新下拉按钮图标 + 菜单选中态）
        layoutBtns.forEach(b => {
            b.classList.toggle('active', b.dataset.layout === currentLayout);
        });
        if (layoutDropdownIcon) {
            layoutDropdownIcon.className = 'icon icon-' + currentLayout;
        }
        // 应用恢复的提示词版本计数状态
        if (showPromptCount && btnShowPromptCount) {
            btnShowPromptCount.classList.add('active');
        } else if (!showPromptCount && btnShowPromptCount) {
            btnShowPromptCount.classList.remove('active');
        }
        // 应用恢复的惯性滚动状态
        if (btnInertiaToggle) {
            btnInertiaToggle.classList.toggle('active', _inertiaEnabled);
        }

        if (callbacks) {
            onImageClick = callbacks.onImageClick;
            onSelectionChange = callbacks.onSelectionChange;

            onEditModeChange = callbacks.onEditModeChange;
            if (callbacks.onFolderTreeChange) {
                onFolderTreeChange = callbacks.onFolderTreeChange;
            }
        }

        bindEvents();
        initIntersectionObserver();

        // 初始化收藏缓存
        refreshFavoriteCache();

        // 监听 Go 后端扫描完成事件，自动刷新当前文件夹
        try {
            if (window.runtime && window.runtime.EventsOn) {
                let pendingScanFolder = null;  // 正在为这个文件夹进行首次加载
                let loadedScanFolder = null;   // 已经为这个文件夹的 scan 加载完成
                window.runtime.EventsOn('scan:complete', (data) => {
                    console.log('[Gallery] 收到扫描完成事件:', data);
                    folderCacheMeta = {};
                    const scanRoot = (data.rootPath || '').replace(/\\/g, '/');
                    if (currentFolderFilter && scanRoot &&
                        currentFolderFilter.replace(/\\/g, '/').startsWith(scanRoot)) {
                        // 正在首次加载中，或已完成加载 → 跳过 forceRefresh
                        if (pendingScanFolder === currentFolderFilter ||
                            loadedScanFolder === currentFolderFilter) {
                            console.log('[Gallery] 已为该 scan 加载或正在加载，跳过重复刷新');
                        } else {
                            console.log('[Gallery] 扫描完成，强制刷新当前文件夹');
                            filterByFolder(currentFolderFilter, null, { forceRefresh: true });
                        }
                    }
                    // ★ 轻量更新：只更新 scanRoot 对应节点的计数，不重建整棵树
                    if (typeof Sidebar !== 'undefined' && Sidebar._updateSingleFolderCount) {
                        Sidebar._updateSingleFolderCount(scanRoot);
                    } else if (typeof Sidebar !== 'undefined' && Sidebar.refreshFolderTree) {
                        Sidebar.refreshFolderTree();
                    }
                });
                // 开始加载时设 pending，完成后移入 loaded
                Gallery._beginScanLoad = (fp) => { pendingScanFolder = fp; loadedScanFolder = null; };
                Gallery._finishScanLoad = (fp) => { if (pendingScanFolder === fp) { pendingScanFolder = null; loadedScanFolder = fp; } };

                // ★ 增量扫描：每完成一个文件夹立即收到推送
                window.runtime.EventsOn('scan:batch', (data) => {
                    const batchRoot = (data.rootPath || '').replace(/\\/g, '/');
                    const count = data.count || 0;
                    if (count === 0) return;

                    console.log('[Gallery] scan:batch root=' + batchRoot + ' folder=' + (data.folder || '(root)') + ' count=' + count + ' totalSoFar=' + data.totalSoFar);

                    // ★ 轻量更新侧栏计数
                    if (typeof Sidebar !== 'undefined' && Sidebar._updateFolderCounts) {
                        Sidebar._updateFolderCounts(batchRoot, data.totalSoFar);
                    } else if (typeof Sidebar !== 'undefined' && Sidebar._updateSingleFolderCount) {
                        Sidebar._updateSingleFolderCount(batchRoot);
                    } else if (typeof Sidebar !== 'undefined' && Sidebar.refreshFolderTree) {
                        Sidebar.refreshFolderTree();
                    }
                });

                // ★ 扫描开始事件：显示扫描状态
                window.runtime.EventsOn('scan:start', (data) => {
                    const rootPath = (data.rootPath || '').replace(/\\/g, '/');
                    console.log('[Gallery] scan:start root=' + rootPath);
                    showScanningPlaceholder();
                    // ★ 轻量更新侧栏显示新添加的文件夹
                    if (typeof Sidebar !== 'undefined' && Sidebar._updateSingleFolderCount) {
                        Sidebar._updateSingleFolderCount(rootPath);
                    } else if (typeof Sidebar !== 'undefined' && Sidebar.refreshFolderTree) {
                        Sidebar.refreshFolderTree();
                    }
                });

                // ★ 文件夹添加事件：新文件夹立即出现在侧边栏
                window.runtime.EventsOn('folder:added', (data) => {
                    const rootPath = (data.rootPath || '').replace(/\\/g, '/');
                    console.log('[Gallery] folder:added root=' + rootPath);
                    if (typeof Sidebar !== 'undefined' && Sidebar._updateSingleFolderCount) {
                        Sidebar._updateSingleFolderCount(rootPath);
                    } else if (typeof Sidebar !== 'undefined' && Sidebar.refreshFolderTree) {
                        Sidebar.refreshFolderTree();
                    }
                });
            }
        } catch (e) {
            console.warn('[Gallery] 注册扫描完成事件失败:', e);
        }

        // 监听标签变更事件，刷新画廊中的标签显示
        window.addEventListener('tags-changed', () => {
            render();
        });
    }

    function bindEvents() {
        // 布局下拉：按钮切换菜单
        layoutDropdownBtn.addEventListener('click', (e) => {
            e.stopPropagation();
            layoutDropdownMenu.classList.toggle('open');
        });
        // 点击菜单外部关闭
        document.addEventListener('click', () => {
            layoutDropdownMenu.classList.remove('open');
        });
        // 菜单选项点击
        layoutBtns.forEach(opt => {
            opt.addEventListener('click', () => {
                const newLayout = opt.dataset.layout;
                if (newLayout !== currentLayout) {
                    masonryLayoutVersion = 0; // 布局切换 → 强制重算
                    pinterestLayoutVersion = 0;
                    listLayoutVersion = 0;
                    renderedRange = { start: -1, end: -1 }; // ★ 强制全量重建
                }
                currentLayout = newLayout;
                // 更新下拉按钮图标
                if (layoutDropdownIcon) {
                    layoutDropdownIcon.className = 'icon icon-' + newLayout;
                    // 清除 initIcons() 设置的旧内联背景图，让 CSS 类生效
                    layoutDropdownIcon.style.backgroundImage = '';
                }
                // 更新菜单选中态
                layoutBtns.forEach(b => b.classList.toggle('active', b.dataset.layout === newLayout));
                if (typeof Storage !== 'undefined' && Storage.setSetting) {
                    Storage.setSetting('galleryLayout', newLayout);
                }
                render();
            });
        });

        thumbnailSlider.addEventListener('input', () => {
            thumbnailSize = parseInt(thumbnailSlider.value);
            thumbnailSizeValue.textContent = thumbnailSize + 'px';
            updateGridColumns();
            masonryLayoutVersion = 0; // 行高变化 → 重算瀑布流布局
            pinterestLayoutVersion = 0; // 缩略图大小变化 → 重算竖版瀑布流布局
            listLayoutVersion = 0; // 缩略图大小变化 → 重算列表布局
            // 强制 grid 全量重建，否则 renderGrid 的滞回守卫会因为 scrollTop 未变而跳过渲染
            if (currentLayout === 'grid') {
                renderedRange = { start: -1, end: -1 };
            }
            if (typeof Storage !== 'undefined' && Storage.setSetting) {
                Storage.setSetting('thumbnailSize', thumbnailSize);
            }
            render();
        });

        sortSelect.addEventListener('change', () => {
            sortOrder = sortSelect.value;
            if (typeof Storage !== 'undefined' && Storage.setSetting) {
                Storage.setSetting('sortOrder', sortOrder);
            }

            // ★ 服务端路径：排序切换后需从后端重新拉取，否则只有已加载的部分参与排序
            if (currentFolderFilter && importedRoots.some(r => {
                const rootId = (r.rootId || '').replace(/\\/g, '/');
                const folderNorm = (currentFolderFilter || '').replace(/\\/g, '/');
                return folderNorm === rootId || folderNorm.startsWith(rootId + '/');
            })) {
                // 清空已加载的服务端数据，重置偏移量，重新加载
                images = images.filter(img => !img._fromServer);
                invalidatePathIndex();
                folderLoadOffset = 0;
                folderLoadTotal = 0;
                delete folderCacheMeta[(currentFolderFilter || '').replace(/\\/g, '/')];
                filterByFolder(currentFolderFilter, null, { forceRefresh: false });
                return;
            }

            sortImages();
            render();
        });

        if (btnShowPromptCount) {
            btnShowPromptCount.addEventListener('click', togglePromptCount);
        }

        if (btnInertiaToggle) {
            btnInertiaToggle.addEventListener('click', toggleInertia);
        }

        // 滚动：每帧立即更新虚拟滚动，Observer 重连在滚动停止后的下一个宏任务执行
        let _scrollRafPending = false;
        let _observerDebounceTimer = null;

        galleryScroll.addEventListener('scroll', () => {
            scrollTop = galleryScroll.scrollTop;
            containerHeight = galleryScroll.clientHeight;

            // ★ 惯性滚动中：消耗一次性令牌，不取消动画
            if (_inertiaScrolling) {
                _inertiaScrolling = false;
            } else if (_inertiaId) {
                // 非惯性触发的 scroll（手动拖滚动条等）→ 取消惯性动画
                cancelAnimationFrame(_inertiaId);
                _inertiaId = null;
                _inertiaV = 0;
            }

            _isScrolling = true;

            // ★ 滚动时冻结卡片 transition，减少样式重算
            if (!galleryGrid.classList.contains('is-scrolling')) {
                galleryGrid.classList.add('is-scrolling');
            }

            // ★ 每帧只触发一次虚拟滚动更新（rAF 合并同一帧内多次 scroll 事件）
            if (!_scrollRafPending) {
                _scrollRafPending = true;
                requestAnimationFrame(() => {
                    _scrollRafPending = false;
                    if (currentLayout === 'grid' || currentLayout === 'masonry' || currentLayout === 'pinterest' || currentLayout === 'list') {
                        updateVirtualScroll();
                    }
                    checkLoadMoreOnScroll();
                });
            }

            // ★ Observer 重连：滚动真正停止 150ms 后才执行，避免滚动中频繁触发
            if (_observerDebounceTimer) clearTimeout(_observerDebounceTimer);
            _observerDebounceTimer = setTimeout(() => {
                _isScrolling = false;
                _observerDebounceTimer = null;
                galleryGrid.classList.remove('is-scrolling');
                _resumeImageObserver();
            }, 150);
        });

        // ★ 惯性滚动：velocity += deltaY * ACCELERATION → scrollBy(0, velocity) → friction
        galleryScroll.addEventListener('wheel', (e) => {
            if (!_inertiaEnabled) return;
            if (e.buttons === 1) return;
            e.preventDefault();
            _inertiaV += e.deltaY * ACCELERATION;
            _inertiaV = Math.max(-MAX_VELOCITY, Math.min(MAX_VELOCITY, _inertiaV));
            _lastWheelTime = performance.now();

            if (!_inertiaId) {
                _lastFrameTime = performance.now();
                _inertiaId = requestAnimationFrame(_tick);
            }
        }, { passive: false });

        // ★ 右键空白区域 → 惯性滚动参数调节（需开启惯性滚动）
        galleryScroll.addEventListener('contextmenu', (e) => {
            if (!_inertiaEnabled) return;
            // 只在点击空白区域时弹出（卡片有自己的右键菜单）
            if (e.target.closest('.image-card')) return;
            e.preventDefault();
            _showInertiaDialog(e.clientX, e.clientY);
        });

        // ★ 拖拽滚动：按住左键拖拽空白区域丝滑滚动
        galleryScroll.addEventListener('pointerdown', (e) => {
            // 只在左键、非卡片区域触发
            if (e.button !== 0) return;
            if (e.target.closest('.image-card')) return;
            if (e.target.closest('button, a, input, textarea, select')) return;

            _dragScroll.active = true;
            _dragScroll.pointerId = e.pointerId;
            _dragScroll.startY = e.clientY;
            _dragScroll.startScroll = galleryScroll.scrollTop;
            _dragScroll.lastY = e.clientY;
            _dragScroll.lastTime = performance.now();
            _dragScroll.velocity = 0;
            cancelAnimationFrame(_dragScroll.rafId);

            galleryScroll.setPointerCapture(e.pointerId);
            galleryScroll.style.cursor = 'grabbing';
            galleryScroll.style.userSelect = 'none';
            e.preventDefault();
        });

        galleryScroll.addEventListener('pointermove', (e) => {
            if (!_dragScroll.active || e.pointerId !== _dragScroll.pointerId) return;

            const dy = e.clientY - _dragScroll.startY;
            const now = performance.now();
            const dt = now - _dragScroll.lastTime;
            if (dt > 0) {
                _dragScroll.velocity = (e.clientY - _dragScroll.lastY) / dt;
            }
            _dragScroll.lastY = e.clientY;
            _dragScroll.lastTime = now;

            galleryScroll.scrollTop = _dragScroll.startScroll - dy;
        });

        function _endDragScroll() {
            if (!_dragScroll.active) return;
            _dragScroll.active = false;
            galleryScroll.style.cursor = '';
            galleryScroll.style.userSelect = '';

            // 惯性滑行
            const v = _dragScroll.velocity;
            if (Math.abs(v) > 0.1) {
                const friction = 0.92;
                let vel = v * 16; // 转为每帧速度
                function glide() {
                    vel *= friction;
                    galleryScroll.scrollBy(0, -vel);
                    if (Math.abs(vel) > 0.3) {
                        _dragScroll.rafId = requestAnimationFrame(glide);
                    }
                }
                _dragScroll.rafId = requestAnimationFrame(glide);
            }
        }

        galleryScroll.addEventListener('pointerup', (e) => {
            if (e.pointerId !== _dragScroll.pointerId) return;
            _endDragScroll();
        });

        galleryScroll.addEventListener('pointerleave', (e) => {
            if (e.pointerId !== _dragScroll.pointerId) return;
            _endDragScroll();
        });

        galleryScroll.addEventListener('lostpointercapture', () => {
            _endDragScroll();
        });

        document.addEventListener('keydown', (e) => {
            if (e.ctrlKey && e.key === 'a') {
                // 焦点在输入框内 → 放行浏览器默认全选行为
                const el = document.activeElement;
                if (el && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' || el.isContentEditable)) return;
                e.preventDefault();
                selectAll();
            }
            if (e.key === 'Escape') {
                clearSelection();
            }
        });

        // 全局点击关闭标签下拉菜单
        document.addEventListener('click', (e) => {
            if (!e.target.closest('.card-tag-dropdown') && !e.target.closest('.tag-btn')) {
                document.querySelectorAll('.card-tag-dropdown.show').forEach(d => {
                    d.classList.remove('show');
                });
            }
        });

        // resize → masonry 布局重算
        let resizeDebounce = 0;
        window.addEventListener('resize', () => {
            clearTimeout(resizeDebounce);
            resizeDebounce = setTimeout(() => {
                if (currentLayout === 'masonry') {
                    masonryLayoutVersion = 0; // 强制重算
                    render();
                } else if (currentLayout === 'pinterest') {
                    pinterestLayoutVersion = 0;
                    render();
                } else if (currentLayout === 'list') {
                    listLayoutVersion = 0;
                    render();
                }
            }, 150);
        });
    }

    async function togglePromptCount() {
        showPromptCount = !showPromptCount;
        if (typeof Storage !== 'undefined' && Storage.setSetting) {
            Storage.setSetting('showPromptCount', showPromptCount);
        }
        if (btnShowPromptCount) {
            btnShowPromptCount.classList.toggle('active', showPromptCount);
        }
        if (!showPromptCount) {
            promptCountMap = {};
        }
        renderedRange = { start: -1, end: -1 };
        render();
    }

    function toggleInertia() {
        _inertiaEnabled = !_inertiaEnabled;
        if (typeof Storage !== 'undefined' && Storage.setSetting) {
            Storage.setSetting('inertiaEnabled', _inertiaEnabled);
        }
        if (btnInertiaToggle) {
            btnInertiaToggle.classList.toggle('active', _inertiaEnabled);
        }
        // 关闭时取消正在运行的惯性动画
        if (!_inertiaEnabled && _inertiaId) {
            cancelAnimationFrame(_inertiaId);
            _inertiaId = null;
            _inertiaV = 0;
            _inertiaScrolling = false;
        }
    }

    async function getPromptCount(imagePath) {
        if (promptCountMap.hasOwnProperty(imagePath)) {
            return promptCountMap[imagePath];
        }
        try {
            const versions = await Storage.getPromptVersions(imagePath);
            promptCountMap[imagePath] = versions.length;
            return versions.length;
        } catch (e) {
            return 0;
        }
    }

    function initIntersectionObserver() {
        // rootMargin：视窗上下各扩展 3 屏，保证图片在进入视野前已经完成下载
        function _getPreloadMargin() {
            const vh = galleryScroll ? galleryScroll.clientHeight : window.innerHeight;
            return Math.max(vh * 3, 1200);
        }

        function _createObserver() {
            const margin = _getPreloadMargin();
            return new IntersectionObserver((entries) => {
                for (const entry of entries) {
                    if (entry.isIntersecting) {
                        const img = entry.target;
                        const dataSrc = img.dataset.src;
                        if (dataSrc) {
                            img.src = dataSrc;
                            img.removeAttribute('data-src');
                        }
                        intersectionObserver.unobserve(img);
                    }
                }
            }, {
                root: galleryScroll,
                rootMargin: `${margin}px 0px`,
                threshold: 0
            });
        }

        intersectionObserver = _createObserver();

        // 缩略图大小变化时重建 observer（rootMargin 需跟着变）
        window._rebuildImageObserver = function() {
            if (intersectionObserver) intersectionObserver.disconnect();
            intersectionObserver = _createObserver();
            galleryGrid.querySelectorAll('img[data-src]').forEach(img => {
                intersectionObserver.observe(img);
            });
        };
    }

    function _pauseImageObserver() {
        // 故意留空：不再 disconnect
    }

    /**
     * 滚动停止后：把新进入 DOM 但还未被 observe 的图片注册到 Observer。
     * 同时对视窗前方 3 屏内的图片直接赋 src，跳过 Observer 回调延迟。
     */
    function _resumeImageObserver() {
        if (!intersectionObserver) return;
        const vh = galleryScroll ? galleryScroll.clientHeight : window.innerHeight;
        const preloadBottom = galleryScroll.scrollTop + vh * 4; // 视窗下方 4 屏内直接加载

        galleryGrid.querySelectorAll('img[data-src]').forEach(img => {
            // 用卡片的 offsetTop 判断是否在预加载范围内（比 getBoundingClientRect 快）
            const cardTop = img.closest('.image-card')?.offsetTop ?? img.offsetTop;
            if (cardTop < preloadBottom) {
                // 在预加载范围内：直接赋 src，不等 Observer 回调
                img.src = img.dataset.src;
                img.removeAttribute('data-src');
                intersectionObserver.unobserve(img);
            } else {
                intersectionObserver.observe(img);
            }
        });
    }

    /**
     * ★ 惯性滚动引擎 — 先移动再摩擦（首帧不吃衰减）
     */
    function _tick(now) {
        const deltaTime = now - _lastFrameTime;
        _lastFrameTime = now;

        // 速度太小就停止
        if (Math.abs(_inertiaV) < MIN_VELOCITY) {
            _inertiaV = 0;
            _inertiaId = null;
            _inertiaScrolling = false;
            return;
        }

        // ★ 一次性令牌：保护本次 scrollBy 触发的 scroll 事件不被取消
        _inertiaScrolling = true;
        galleryScroll.scrollBy(0, _inertiaV);

        // 滚轮停止超过 RELEASE_DELAY ms → 切换为松手摩擦（快速衰减）
        const friction = (now - _lastWheelTime > RELEASE_DELAY)
            ? FRICTION_IDLE
            : FRICTION_ACTIVE;
        _inertiaV *= friction;

        // 继续下一帧
        _inertiaId = requestAnimationFrame(_tick);
    }

    /**
     * ★ 右键弹出 → 惯性滚动参数调节对话框
     */
    function _showInertiaDialog(mx, my) {
        const overlay = document.getElementById('modalOverlay');
        const content = document.getElementById('modalContent');

        const t = (typeof I18n !== 'undefined' ? I18n.t : (s) => s);

        content.innerHTML = `
            <div class="settings-dialog">
                <h2>${t('inertia.title')}</h2>
                <div class="inertia-params">
                    <label><span>${t('inertia.accel')}</span><input type="number" id="ipAccel" value="${ACCELERATION}" step="0.5" min="0"></label>
                    <label><span>${t('inertia.fric_active')}</span><input type="number" id="ipFricActive" value="${FRICTION_ACTIVE}" step="0.01" min="0" max="1"></label>
                    <label><span>${t('inertia.fric_idle')}</span><input type="number" id="ipFricIdle" value="${FRICTION_IDLE}" step="0.01" min="0" max="1"></label>
                    <label><span>${t('inertia.max_vel')}</span><input type="number" id="ipMaxVel" value="${MAX_VELOCITY}" step="10" min="0"></label>
                    <label><span>${t('inertia.min_vel')}</span><input type="number" id="ipMinVel" value="${MIN_VELOCITY}" step="0.1" min="0"></label>
                    <label><span>${t('inertia.release_delay')}</span><input type="number" id="ipReleaseDelay" value="${RELEASE_DELAY}" step="10" min="0"></label>
                </div>
            </div>
            <div class="modal-actions">
                <button id="btnApplyInertia" class="btn-primary">${t('inertia.apply')}</button>
                <button id="btnCloseInertia" class="btn-secondary">${t('inertia.cancel')}</button>
            </div>
        `;

        overlay.style.display = 'flex';

        const close = () => { overlay.style.display = 'none'; };

        document.getElementById('btnCloseInertia').addEventListener('click', close);
        overlay.addEventListener('click', (e) => {
            if (e.target === overlay) close();
        });

        document.getElementById('btnApplyInertia').addEventListener('click', () => {
            ACCELERATION = parseFloat(document.getElementById('ipAccel').value) || 20;
            FRICTION_ACTIVE = parseFloat(document.getElementById('ipFricActive').value) || 0.9;
            FRICTION_IDLE = parseFloat(document.getElementById('ipFricIdle').value) || 0.5;
            MAX_VELOCITY = parseFloat(document.getElementById('ipMaxVel').value) || 500;
            MIN_VELOCITY = parseFloat(document.getElementById('ipMinVel').value) || 0.5;
            RELEASE_DELAY = parseFloat(document.getElementById('ipReleaseDelay').value) || 80;
            // ★ 持久化参数
            if (typeof Storage !== 'undefined' && Storage.setSetting) {
                Storage.setSetting('inertiaAccel', ACCELERATION);
                Storage.setSetting('inertiaFricActive', FRICTION_ACTIVE);
                Storage.setSetting('inertiaFricIdle', FRICTION_IDLE);
                Storage.setSetting('inertiaMaxVel', MAX_VELOCITY);
                Storage.setSetting('inertiaMinVel', MIN_VELOCITY);
                Storage.setSetting('inertiaReleaseDelay', RELEASE_DELAY);
            }
            close();
        });
    }

    /**
     * ★ 触发分页加载前调用：中止视口外正在传输中的图片请求，释放 HTTP 连接槽。
     *   视口内已在加载的图片不受影响，保证用户当前看到的内容不闪烁。
     */
    function _abortOutOfViewportLoads() {
        const galleryRect = galleryScroll.getBoundingClientRect();

        // ★ 增加上下各 1.5 倍视窗高度的缓冲带，防止误杀预加载区域的卡片
        const buffer = galleryRect.height * 1.5;
        const viewTop = galleryRect.top - buffer;
        const viewBottom = galleryRect.bottom + buffer;

        // 找出有 src（正在加载中）且位于视口外的图片
        const loadingImgs = galleryGrid.querySelectorAll('img[src]:not([src=""])');
        for (const img of loadingImgs) {
            // 跳过已完成加载的图片（naturalWidth > 0 表示已解码完成）
            if (img.complete && img.naturalWidth > 0) continue;
            const rect = img.getBoundingClientRect();
            // 使用带缓冲的边界判定
            const inViewport = rect.bottom >= viewTop && rect.top <= viewBottom;
            if (!inViewport) {
                // 中止：把 src 存回 data-src，清空 src 释放连接槽
                if (!img.dataset.src) {
                    img.dataset.src = img.src;
                }
                img.src = '';
                img.classList.add('loading');
            }
        }
    }

    /**
     * ★ 缩略图预温热：在当前渲染范围之外的即将进入视窗的图片，
     *   用离屏 Image 对象提前发起 HTTP 请求，填充浏览器缓存。
     *   当虚拟滚动创建这些卡片时，缩略图已在缓存中，瞬间显示。
     */
    function _prewarmThumbnails(displayImages, currentStart, currentEnd) {
        if (!displayImages || displayImages.length === 0) return;
        const prewarmCount = Math.min(OVERSCAN * getColumnCount(), 100);
        // 预加载当前渲染窗口后方 prewarmCount 张
        const prewarmStart = currentEnd;
        const prewarmEnd = Math.min(displayImages.length, currentEnd + prewarmCount);
        for (let i = prewarmStart; i < prewarmEnd; i++) {
            const imgData = displayImages[i];
            if (imgData && imgData.thumbnailUrl) {
                const preImg = new Image();
                preImg.decoding = 'async';
                preImg.src = imgData.thumbnailUrl;
            }
        }
    }



    /**
     * 按需加载指定文件夹路径下的所有图片
     * 遍历 filteredImages（已按 currentFolderFilter 过滤），
     * 为其中未加载的图片创建 ObjectURL
     * @param {stream} folderPath - 文件夹路径（用于进度回调）
     * @param {Array} targetImages - 要加载的图片数组（默认 filteredImages）
     * @returns {Promise<Array>} 加载完成后的图片数组
     */
    async function lazyLoadFolder(folderPath, targetImages) {
        const imgs = targetImages || (isFilteringActive ? filteredImages : images);
        const unloaded = imgs.filter(img => !img._loaded && img.file);
        
        if (unloaded.length === 0) {
            // 都已加载，直接返回
            return imgs;
        }

        showLoading(true);

        const BATCH_SIZE = getOptimalBatchSize();

        for (let i = 0; i < unloaded.length; i += BATCH_SIZE) {
            const batch = unloaded.slice(i, i + BATCH_SIZE);
            
            // 使用 setTimeout 让出主线程，避免卡顿
            await new Promise(resolve => setTimeout(resolve, 0));

            for (const img of batch) {
                try {
                    const objectUrl = URL.createObjectURL(img.file);
                    img.url = objectUrl;
                    img.thumbnailUrl = objectUrl;
                    img._loaded = true;
                } catch (err) {
                    console.warn('[Gallery] 创建 ObjectURL 失败:', img.name, err);
                }

            }

        }

        showLoading(false);

        return imgs;
    }

    // ==================== 从后端加载图片 ====================

    /**
     * 从后端 API 加载图片列表
     * Go 后端扫描文件系统后，前端通过此方法获取图片数据
     * 图片通过 /image/{id} URL 直接显示，无需创建 ObjectURL
     * @param {string} folderPath - 可选，按文件夹过滤
     * @returns {Promise<Array>} 图片列表
     */
    // ★ Bug 4b: 缓存 httpBaseURL，避免每次加载都重新获取
    let cachedHttpBaseURL = '';
    let cachedImageBaseURL = ''; // ★ 原图独立 origin
    let cachedThumbGen = 0;

    // ★ 复用 WailsBridge 启动时预热的 baseURL，避免重复 RPC 与重试循环
    async function ensureBaseURLs() {
        if (typeof WailsBridge === 'undefined' || !WailsBridge.isWails()) return;
        if (!cachedHttpBaseURL) {
            cachedHttpBaseURL = WailsBridge._httpBaseURL || '';
            if (!cachedHttpBaseURL) {
                try { cachedHttpBaseURL = await WailsBridge.getHTTPBaseURL(); } catch (e) {}
            }
        }
        if (!cachedImageBaseURL) {
            cachedImageBaseURL = WailsBridge._imageBaseURL || '';
            if (!cachedImageBaseURL) {
                try { cachedImageBaseURL = await WailsBridge.getImageBaseURL(); } catch (e) {}
            }
        }
    }

    function makeThumbURL(imgID, lastModified) {
        return cachedHttpBaseURL + '/thumb/' + imgID + '?t=' + lastModified + '&g=' + cachedThumbGen;
    }

    async function refreshThumbGen() {
        if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
            cachedThumbGen = await WailsBridge.getThumbGeneration();
        }
        // 更新所有图片对象的 thumbnailUrl
        for (const img of images) {
            if (img.id && img.lastModified) {
                img.thumbnailUrl = makeThumbURL(img.id, img.lastModified);
            }
        }
        // 更新 DOM 中所有缩略图 URL 的 &g= 参数，立即触发浏览器重新请求
        const newGen = '&g=' + cachedThumbGen;
        galleryGrid.querySelectorAll('img[src*="/thumb/"], img[data-src*="/thumb/"]').forEach(img => {
            if (img.src && img.src.includes('/thumb/')) {
                img.src = img.src.replace(/&g=\d+/, newGen);
            }
            const ds = img.dataset.src;
            if (ds && ds.includes('/thumb/')) {
                img.dataset.src = ds.replace(/&g=\d+/, newGen);
            }
        });
    }

    async function loadImagesFromServer(folderPath, offset = 0, limit = 0) {
        try {
            let serverImages, total;
            const isWails = typeof WailsBridge !== 'undefined' && WailsBridge.isWails();

            if (isWails) {
                const result = await WailsBridge.getImages({ folder: folderPath || '', offset, limit, sortOrder: sortOrder || 'date-desc' });
                serverImages = result.items || result;
                total = result.total || 0;
                await ensureBaseURLs();
                if (!cachedHttpBaseURL) {
                    console.error('[Gallery] 获取 HTTP 服务器地址失败');
                    return { images: [], total: 0 };
                }
            } else {
                let url = '/api/images';
                const params = [];
                if (folderPath) {
                    params.push('folder=' + encodeURIComponent(folderPath));
                }
                if (offset) params.push('offset=' + offset);
                if (limit) params.push('limit=' + limit);
                if (sortOrder) params.push('sortOrder=' + encodeURIComponent(sortOrder));
                if (params.length > 0) {
                    url += '?' + params.join('&');
                }
                const response = await fetch(url);
                if (!response.ok) {
                    throw new Error('获取图片列表失败 (HTTP ' + response.status + ')');
                }
                const json = await response.json();
                serverImages = json.items || json;
                total = json.total || 0;
            }

            const imageList = Array.isArray(serverImages) ? serverImages : (serverImages.items || []);
            const len = imageList.length;
            const frontendImages = new Array(len);
            for (let i = 0; i < len; i++) {
                const img = imageList[i];
                const imageURL = isWails ? ((cachedImageBaseURL || cachedHttpBaseURL) + '/image/' + img.id) : '/image/' + img.id;
                const thumbURL = isWails ? (makeThumbURL(img.id, img.lastModified)) : '/thumb/' + img.id + '?t=' + (img.lastModified || 0);
                
                frontendImages[i] = {
                    id: img.id,
                    path: img.path,
                    name: img.name,
                    size: img.size,
                    lastModified: img.lastModified,
                    createdAt: img.createdAt || 0,
                    folder: img.folder,
                    rootPath: img.rootPath,
                    rootId: img.rootPath,
                    url: imageURL,
                    thumbnailUrl: thumbURL,
                    width: img.width || 0,
                    height: img.height || 0,
                    displayName: img.rootPath ? img.rootPath.split(/[\\/]/).pop() : '',
                    metadata: null,
                    file: null,
                    _loaded: true,
                    _fromServer: true,
                    isVideo: img.isVideo || false
                };
            }

            return { images: frontendImages, total: total || frontendImages.length };
        } catch (err) {
            console.warn('[Gallery] 从后端加载图片失败:', err.message);
            return { images: [], total: 0 };
        }
    }

    /**
     * 根据路径列表精确加载图片（用于标签/收藏过滤，避免全量查询）
     * 与 loadImagesFromServer 共享 URL 构造逻辑，仅数据源不同
     */
    async function loadImagesByPaths(paths, offset = 0, limit = 0) {
        try {
            let serverImages, total;
            const isWails = typeof WailsBridge !== 'undefined' && WailsBridge.isWails();

            if (isWails) {
                const result = await WailsBridge.getImagesByPaths(paths, offset, limit, sortOrder || 'date-desc');
                serverImages = result.items || result;
                total = result.total || 0;
                await ensureBaseURLs();
            } else {
                const response = await fetch('/api/images-by-paths', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ paths, offset, limit, sortOrder: sortOrder || '' })
                });
                if (!response.ok) {
                    throw new Error('获取图片列表失败 (HTTP ' + response.status + ')');
                }
                const json = await response.json();
                serverImages = json.items || json;
                total = json.total || 0;
            }

            const imageList = Array.isArray(serverImages) ? serverImages : (serverImages.items || []);
            const len = imageList.length;
            const frontendImages = new Array(len);
            for (let i = 0; i < len; i++) {
                const img = imageList[i];
                const imageURL = isWails ? ((cachedImageBaseURL || cachedHttpBaseURL) + '/image/' + img.id) : '/image/' + img.id;
                const thumbURL = isWails ? (makeThumbURL(img.id, img.lastModified)) : '/thumb/' + img.id + '?t=' + (img.lastModified || 0);
                frontendImages[i] = {
                    id: img.id,
                    path: img.path,
                    name: img.name,
                    size: img.size,
                    lastModified: img.lastModified,
                    createdAt: img.createdAt || 0,
                    folder: img.folder,
                    rootPath: img.rootPath,
                    rootId: img.rootPath,
                    url: imageURL,
                    thumbnailUrl: thumbURL,
                    width: img.width || 0,
                    height: img.height || 0,
                    displayName: img.rootPath ? img.rootPath.split(/[\\/]/).pop() : '',
                    metadata: null,
                    file: null,
                    _loaded: true,
                    _fromServer: true,
                    isVideo: img.isVideo || false
                };
            }

            return { images: frontendImages, total: total || frontendImages.length };
        } catch (err) {
            console.warn('[Gallery] 按路径加载图片失败:', err.message);
            return { images: [], total: 0 };
        }
    }

    /**
     * 将后端加载的图片合并到全局 images 数组中
     * ★ 优化：使用 Map 索引代替 Set + 线性扫描
     * @param {Array} serverImages - 从后端加载的图片列表
     */
    function mergeServerImages(serverImages) {
        if (!serverImages || serverImages.length === 0) return;

        // ★ 使用 Map 建立路径索引，O(1) 查找
        if (!images._pathIndex) {
            images._pathIndex = new Map();
            for (let i = 0; i < images.length; i++) {
                images._pathIndex.set(images[i].path, i);
            }
        }
        
        let addedCount = 0;
        let updatedCount = 0;
        for (const img of serverImages) {
            const existingIdx = images._pathIndex.get(img.path);
            if (existingIdx !== undefined) {
                // 路径相同时，同步最新的 id 和 url（使用稳定ID后理论上不变，但双保险）
                images[existingIdx].id  = img.id;
                images[existingIdx].url = img.url;
                images[existingIdx].thumbnailUrl = img.thumbnailUrl;
                updatedCount++;
            } else {
                const idx = images.length;
                images.push(img);
                images._pathIndex.set(img.path, idx);
                addedCount++;
            }
        }
        
        if (addedCount > 0 || updatedCount > 0) {
            console.log(`[Gallery] 合并了 ${addedCount} 张后端图片到全局列表，更新了 ${updatedCount} 张已有图片`);
            applyCurrentFilter();
            sortImages();
            updateImageCount();
        }
    }

    /**
     * ★ 使路径索引失效（当 images 数组被重新排序或大量修改后调用）
     */
    function invalidatePathIndex() {
        delete images._pathIndex;
    }

    /**
     * ★ 以"重新导入"方式刷新指定根目录：清除该根的旧图片，从后端全量加载并合并
     *    等效于移除文件夹后重新添加，但保留 importedRoots 记录和 displayName
     */
    async function refreshRootFromServer(rootPath) {
        const normalizedRoot = (rootPath || '').replace(/\\/g, '/');
        if (!normalizedRoot) return { count: 0, rootPath };

        // 1. 移除该 rootPath 所有来自后端的旧图片
        images = images.filter(img => {
            if (!img._fromServer) return true;
            const imgRoot = (img.rootPath || '').replace(/\\/g, '/');
            return imgRoot !== normalizedRoot && !imgRoot.startsWith(normalizedRoot + '/');
        });
        invalidatePathIndex();

        // 清除该根目录下所有子文件夹的缓存元数据
        for (const key of Object.keys(folderCacheMeta)) {
            if (key === normalizedRoot || key.startsWith(normalizedRoot + '/')) {
                delete folderCacheMeta[key];
            }
        }

        // 2. 从后端按文件夹过滤加载（避免拉取全部 44 万张图片）
        const { images: allServerImages } = await loadImagesFromServer(rootPath, 0, FOLDER_LOOKAHEAD);

        // 3. 筛选出该根目录下的图片（服务器已按 folder 过滤，此处二次确认）
        const rootImages = allServerImages.filter(img => {
            const imgRoot = (img.rootPath || '').replace(/\\/g, '/');
            return imgRoot === normalizedRoot || imgRoot.startsWith(normalizedRoot + '/');
        });

        // 4. 合并到全局 images 数组
        if (rootImages.length > 0) {
            images.push(...rootImages);
        }

        // 5. 重新应用当前过滤并渲染
        applyCurrentFilter();
        sortImages();
        updateImageCount();

        if (isFilteringActive && filteredImages.length > 100) {
            if (currentLayout === 'masonry') {
                renderMasonry(filteredImages);
            } else if (currentLayout === 'pinterest') {
                renderPinterest(filteredImages);
            } else if (currentLayout === 'list') {
                renderList(filteredImages);
            } else {
                progressiveRender(filteredImages);
            }
        } else if (isFilteringActive) {
            render();
        } else if (images.length > 100) {
            if (currentLayout === 'masonry') {
                renderMasonry(images);
            } else if (currentLayout === 'pinterest') {
                renderPinterest(images);
            } else if (currentLayout === 'list') {
                renderList(images);
            } else {
                progressiveRender(images);
            }
        } else {
            render();
        }

        console.log(`[Gallery] 已从后端重新加载根目录: ${normalizedRoot}，共 ${rootImages.length} 张图片`);
        return { count: rootImages.length, rootPath };
    }

    // ==================== File System Access API 导入 ====================

    /**
     * 使用 showDirectoryPicker() 选择文件夹并导入
     * 优先将真实目录注册到后端 user/registered-roots.json，再由后端扫描并持久化
     * ★ 所有数据只通过后端 user-data.json 持久化，不依赖 IndexedDB
     * ★ 如果浏览器暴露了绝对路径，由 Go 后端扫描文件系统
     * ★ 如果浏览器未暴露绝对路径，由前端遍历 FileSystemDirectoryHandle 获取 File 对象
     */
    async function importFromDirectoryPicker() {
        try {
            if (!window.showDirectoryPicker) {
                showToast(t('toast.fsa_not_supported'), 'error');
                return null;
            }

            const handle = await window.showDirectoryPicker({ mode: 'read' });
            const rootName = handle.name;

            showLoading(true);

            // 优先尝试借助 Electron/Chromium 暴露的绝对路径
            let possiblePath = handle.path || handle.fullPath || null;

            if (!possiblePath) {
                // ★ 浏览器不暴露绝对路径：让用户输入/确认路径
                //    这是 Web 浏览器的安全限制，无法自动获取绝对路径
                //    必须由用户手动提供路径，后端才能扫描文件系统并持久化
                showLoading(false);
                const userPath = prompt(
                    `浏览器安全限制无法自动获取文件夹的绝对路径。\n` +
                    `请输入您刚才选择的 "${rootName}" 文件夹的完整路径：\n\n` +
                    `例如：D:\\images\\${rootName} 或 /home/user/${rootName}\n\n` +
                    `（输入路径后，后端会扫描该文件夹并持久化，刷新不会消失）`
                );
                if (userPath && userPath.trim()) {
                    possiblePath = userPath.trim();
                    showLoading(true);
                } else {
                    // 用户取消了输入，不导入
                    App.showToast(t('toast.no_path_cancelled'), 'info');
                    return null;
                }
            }

            // ★ 有路径（自动获取或用户输入）：由 Go 后端扫描文件系统
            App.showToast(t('toast.scanning_folder').replace('{name}', rootName), 'info');

            let result;
            // ★ Wails 环境：直接调用 Go 函数
            if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
                result = await WailsBridge.scanFolder(possiblePath);
                if (!result || !result.success) {
                    throw new Error((result && result.message) || '注册扫描目录失败');
                }
            } else {
                const response = await fetch('/api/scan-folder', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ path: possiblePath })
                });
                result = await response.json().catch(() => ({}));
                if (!response.ok) {
                    throw new Error(result.error || '注册扫描目录失败');
                }
            }
            const savedRootPath = result.folderPath || possiblePath;
            console.log(`[Gallery] 后端已注册扫描目录: ${savedRootPath}`);

            // ★ 从后端按文件夹过滤加载已扫描的图片列表
            const serverImages = (await loadImagesFromServer(savedRootPath, 0, FOLDER_LOOKAHEAD)).images;
            const rootImages = serverImages.filter(img => img.rootPath === savedRootPath);
            if (rootImages.length > 0) {
                // 移除旧的同路径图片，防止重复
                images = images.filter(img => img.rootPath !== savedRootPath || !img._fromServer);
                images.push(...rootImages);
                console.log(`[Gallery] 从后端加载了 ${rootImages.length} 张图片`);
            }

            // ★ 将文件夹信息添加到 importedRoots
            if (!importedRoots.some(r => r.rootId === savedRootPath)) {
                importedRoots.push({
                    rootId: savedRootPath,
                    path: savedRootPath,
                    name: rootName,
                    handleName: rootName,
                    displayName: rootName
                });
                saveImportedRootsToServer().catch(err => {
                    console.warn('[Gallery] 保存导入文件夹信息到后端失败:', err.message);
                });
            }

            applyCurrentFilter();
            sortImages();
            updateImageCount();
            showLoading(false);

            return {
                importedCount: rootImages.length,
                displayName: rootName,
                savedRootPath
            };
        } catch (err) {
            showLoading(false);
            if (err.name === 'AbortError' || err.message?.includes('abort')) {
                console.log('[Gallery] 用户取消了文件夹选择');
                return null;
            }
            console.error('[Gallery] 导入文件夹失败:', err);
            showToast(t('toast.import_failed') + ': ' + err.message, 'error');
            return null;
        }
    }

    /**
     * 从已保存的 FileSystemDirectoryHandle 恢复图片列表
     * 刷新页面后调用，用户只需授权一次
     */
    async function restoreFromDirectoryHandles() {
        try {
            const savedHandles = await Storage.getAllDirectoryHandles();
            if (!savedHandles || savedHandles.length === 0) {
                console.log('[Gallery] IndexedDB 中没有已保存的目录句柄');
                return 0;
            }

            let totalRestored = 0;
            let restoredRootCount = 0;
            let deniedCount = 0;
            let invalidCount = 0;

            console.log(`[Gallery] 发现 ${savedHandles.length} 个已保存的目录句柄，开始恢复`);

            for (const saved of savedHandles) {
                try {
                    const handle = saved.handle;
                    if (!handle) {
                        invalidCount++;
                        console.warn(`[Gallery] 目录句柄无效，已跳过: ${saved.name}`);
                        continue;
                    }

                    // 请求权限（用户只需点一次"允许"）
                    const options = { mode: 'read' };
                    let permission = await handle.queryPermission(options);
                    if (permission !== 'granted') {
                        permission = await handle.requestPermission(options);
                    }
                    if (permission !== 'granted') {
                        deniedCount++;
                        console.warn(`[Gallery] 用户未授权访问目录: ${saved.name}`);
                        continue;
                    }

                    const rootId = saved.id;
                    const rootName = saved.name;

                    // 检查是否已导入
                    if (importedRoots.some(r => r.rootId === rootId)) continue;

                    const importedImages = [];

                    async function traverseDir(dirHandle, currentPath = '') {
                        const entries = [];
                        try {
                            for await (const entry of dirHandle.values()) {
                                entries.push(entry);
                            }
                        } catch (err) {
                            console.warn(`[Gallery] 无法读取目录 ${currentPath}:`, err.message);
                            return;
                        }

                        for (const entry of entries) {
                            if (entry.kind === 'file') {
                                const ext = '.' + (entry.name.split('.').pop() || '').toLowerCase();
                                if (IMAGE_EXTENSIONS.has(ext)) {
                                    try {
                                        const file = await entry.getFile();
                                        const folder = currentPath ? currentPath : '';
                                        const fullPath = folder ? `${rootName}/${folder}/${entry.name}` : `${rootName}/${entry.name}`;
                                        const relativePath = folder ? `${folder}/${entry.name}` : entry.name;

                                        const imgEntry = {
                                            id: 'local_' + Math.abs(hashString(fullPath + '::' + file.size + '::' + file.lastModified)),
                                            path: fullPath,
                                            relativePath: relativePath,
                                            name: entry.name,
                                            size: file.size || 0,
                                            lastModified: file.lastModified || Date.now(),
                                            folder: folder,
                                            rootPath: rootId,
                                            rootId: rootId,
                                            displayName: rootName,
                                            url: null,
                                            thumbnailUrl: null,
                                            metadata: null,
                                            file: file,
                                            _loaded: false,
                                            _directoryHandle: handle,
                                            _fileHandle: entry
                                        };

                                        importedImages.push(imgEntry);
                                    } catch (err) {
                                        console.warn(`[Gallery] 读取文件失败: ${entry.name}`, err.message);
                                    }
                                }
                            } else if (entry.kind === 'directory') {
                                const subPath = currentPath ? `${currentPath}/${entry.name}` : entry.name;
                                await traverseDir(entry, subPath);
                            }
                        }
                    }

                    await traverseDir(handle);

                    if (importedImages.length > 0) {
                        images.push(...importedImages);
                        importedRoots.push({
                            rootId: rootId,
                            path: rootId,
                            name: rootName,
                            handleName: rootName,
                            displayName: rootName
                        });
                        totalRestored += importedImages.length;
                        restoredRootCount++;
                        console.log(`[Gallery] 已恢复目录: ${rootName} (${importedImages.length} 张图片)`);
                    } else {
                        console.warn(`[Gallery] 目录中未找到可恢复图片: ${rootName}`);
                    }
                } catch (err) {
                    invalidCount++;
                    console.warn(`[Gallery] 恢复目录句柄失败: ${saved.name}`, err.message);
                }
            }

            if (totalRestored > 0) {
                applyCurrentFilter();
                sortImages();
                updateImageCount();
                console.log(`[Gallery] 共恢复 ${restoredRootCount} 个目录，${totalRestored} 张图片`);
            } else {
                console.warn(`[Gallery] 未恢复任何目录。已保存句柄: ${savedHandles.length}，未授权: ${deniedCount}，无效/失败: ${invalidCount}`);
                if (deniedCount > 0) {
                    showToast(t('toast.handle_not_authorized').replace('{n}', savedHandles.length), 'warning');
                } else if (invalidCount > 0) {
                    showToast(t('toast.handle_invalid'), 'warning');
                }
            }

            return totalRestored;
        } catch (err) {
            console.warn('[Gallery] 从 directory handles 恢复失败:', err.message);
            showToast(t('toast.read_folder_failed') + ': ' + err.message, 'warning');
            return 0;
        }
    }

    function hashString(str) {
        let hash = 0;
        for (let i = 0; i < str.length; i++) {
            hash = ((hash << 5) - hash) + str.charCodeAt(i);
            hash |= 0;
        }
        return hash;
    }

    // ==================== 本地文件夹导入（兼容旧版 webkitdirectory） ====================

    async function importFromFileList(fileList) {
        showLoading(true);

        try {
            const files = Array.from(fileList || []);
            const imageFiles = files.filter(isImageFileObject);

            if (imageFiles.length === 0) {
                return { importedCount: 0, skippedCount: files.length, rootPath: null };
            }

            const rootName = getRootNameFromFiles(imageFiles);
            // ★ 生成唯一 rootId，使同名文件夹可以共存
            const rootId = 'root_' + Date.now() + '_' + Math.random().toString(36).substring(2, 8);
            const rootPath = rootId; // 内部使用 rootId 作为标识

            // 不再检查重复，同名文件夹可以共存（不同路径的同名文件夹是不同内容）

            const hasStructuredRelativePaths = imageFiles.some(file => {
                const relativePath = normalizeRelativePath(file.webkitRelativePath || '');
                return relativePath.includes('/');
            });

            const existingPaths = new Set(images.map(img => img.path));
            const importedImages = [];
            for (const file of imageFiles) {
                const rawRelativePath = hasStructuredRelativePaths
                    ? normalizeRelativePath(file.webkitRelativePath || file.name)
                    : file.name;
                const parsed = parseRelativePath(rawRelativePath, rootName, hasStructuredRelativePaths);
                const imagePath = parsed.fullPath;

                if (existingPaths.has(imagePath)) {
                    continue;
                }

                // ★ 按需加载：导入时只记录文件信息，不创建 ObjectURL
                // URL.createObjectURL 会在点击文件夹时才执行
                const imgEntry = {
                    id: createLocalImageId(rawRelativePath, file),
                    path: imagePath,
                    relativePath: parsed.relativePath,
                    name: file.name,
                    size: file.size || 0,
                    lastModified: file.lastModified || Date.now(),
                    folder: parsed.folder,
                    rootPath,       // 内部使用 rootId
                    rootId,         // 唯一根目录 ID
                    displayName: rootName, // 显示名称，默认为文件夹名，可自定义
                    url: null,           // 延迟加载
                    thumbnailUrl: null,   // 延迟加载
                    metadata: null,
                    file,
                    _loaded: false        // 标记是否已加载
                };

                importedImages.push(imgEntry);
            }

            images.push(...importedImages);

            const rootInfo = {
                rootId: rootId,
                path: rootId,         // 树节点统一使用 rootId 作为路径标识
                name: rootName,       // 原始文件夹名
                handleName: rootName,
                displayName: rootName // 显示名称，可自定义
            };
            importedRoots.push(rootInfo);

            // ★ 保存导入的文件夹信息到后端 settings.json（跨浏览器持久化）
            saveImportedRootsToServer().catch(err => {
                console.warn('[Gallery] 保存导入文件夹信息到后端失败:', err.message);
            });

            applyCurrentFilter();
            sortImages();
            updateImageCount();

            // ★ 导入后不渲染图片，只更新文件夹树（由 Sidebar 调用 refreshFolderTree）
            // 清空画廊显示，避免空白
            galleryGrid.innerHTML = '';
            galleryGrid.className = 'gallery-grid';
            showGalleryPlaceholder('请点击左侧文件夹来浏览图片');

            return {
                importedCount: importedImages.length,
                skippedCount: files.length - imageFiles.length,
                rootPath,
                rootId,
                displayName: rootName
            };
        } finally {
            showLoading(false);
        }
    }

    async function reloadImportedRoot(rootPath) {
        // rootPath 可能是 rootId 或原始路径，兼容处理
        const rootImages = images.filter(img => img.rootPath === rootPath || img.rootId === rootPath);
        if (rootImages.length === 0) {
            throw new Error('未找到对应的已导入文件夹');
        }

        const files = rootImages.map(img => img.file).filter(Boolean);
        if (files.length === 0) {
            throw new Error('该文件夹的原始文件对象已不可用，请重新选择文件夹');
        }

        // 释放已加载的 ObjectURL
        for (const img of rootImages) {
            if (img._loaded && img.thumbnailUrl && img.thumbnailUrl.startsWith('blob:')) {
                URL.revokeObjectURL(img.thumbnailUrl);
            }
        }
        images = images.filter(img => img.rootPath !== rootPath && img.rootId !== rootPath);
        if (currentFolderFilter && (currentFolderFilter === rootPath || currentFolderFilter.startsWith(rootPath + '/'))) {
            currentFolderFilter = null;
        }

        return importFromFileList(files);
    }

    async function removeImportedRoot(rootPath) {
        if (!rootPath) return false;

        // ★ 修复 Bug 1：规范化路径比较，解决斜杠风格不一致的问题
        const normalized = (rootPath || '').replace(/\\/g, '/').toLowerCase();

        // rootPath 可能是 rootId 或原始路径，优先规范化匹配
        let toRemove = images.filter(img => {
            const imgRoot = (img.rootPath || '').replace(/\\/g, '/').toLowerCase();
            const imgRootId = (img.rootId || '').replace(/\\/g, '/').toLowerCase();
            return imgRoot === normalized || imgRootId === normalized;
        });

        // ★ 修复 Bug 1：即使 images 中没有图片（后端已删除），也要清理 importedRoots
        //    并且要移除对应的 images（如果存在）
        if (toRemove.length > 0) {
            releaseImagesByRoot(rootPath);
            const removePaths = new Set(toRemove.map(img => img.path));
            images = images.filter(img => !removePaths.has(img.path));
            invalidatePathIndex();

            // 清除该根目录下所有缓存元数据
            for (const key of Object.keys(folderCacheMeta)) {
                if (key === normalized || key.startsWith(normalized + '/')) {
                    delete folderCacheMeta[key];
                }
            }
        }

        // ★ 修复 Bug 1：规范化比较 importedRoots，确保能匹配到后端注册的真实路径
        importedRoots = importedRoots.filter(root => {
            const rId = (root.rootId || '').replace(/\\/g, '/').toLowerCase();
            const rPath = (root.path || '').replace(/\\/g, '/').toLowerCase();
            const rName = (root.name || '').replace(/\\/g, '/').toLowerCase();
            return rId !== normalized && rPath !== normalized && rName !== normalized;
        });

        if (currentFolderFilter) {
            const currentNorm = currentFolderFilter.replace(/\\/g, '/').toLowerCase();
            if (currentNorm === normalized || currentNorm.startsWith(normalized + '/')) {
                currentFolderFilter = null;
            }
        }

        applyCurrentFilter();
        sortImages();
        updateImageCount();
        render();

        // 持久化清理后的 importedRoots
        await saveImportedRootsToServer();

        return true;
    }

    async function loadAll() {
        applyCurrentFilter();
        sortImages();
        
        // 刷新时如果图片未加载，按需加载当前过滤的图片
        const displayImages = isFilteringActive ? filteredImages : images;
        const hasAnyLoaded = displayImages.some(img => img._loaded);
        if (!hasAnyLoaded && displayImages.length > 0) {
            // 不自动加载全部，只更新计数
            updateImageCount();
            return;
        }
        
        render();
    }

    async function loadFromServer(folderPath) {
        // 已废弃，保留兼容
    }

    function hasImages() {
        return images.length > 0;
    }

    function getFolderTree() {
        const roots = [];
        
        // ★ 预构建 rootId -> images 的索引，避免多次 filter
        const rootImageMap = new Map();
        for (const img of images) {
            const rid = img.rootId || img.rootPath;
            if (!rootImageMap.has(rid)) {
                rootImageMap.set(rid, []);
            }
            rootImageMap.get(rid).push(img);
        }
        
        for (const root of importedRoots) {
            const rootImages = rootImageMap.get(root.rootId) || [];
            const rootNode = {
                name: root.name,
                path: root.rootId,
                rootId: root.rootId,
                displayPath: root.displayName || root.name,
                displayName: root.displayName || root.name,
                expanded: false,
                imageCount: rootImages.length,
                isRoot: true,
                children: []
            };

            const nodeMap = new Map();
            nodeMap.set(root.rootId, rootNode);

            // ★ 第一遍：构建树结构 + 直接计数
            // folderPath -> directCount (该文件夹直属的图片数)
            const directCounts = new Map();
            directCounts.set(root.rootId, 0);

            for (const img of rootImages) {
                const folder = img.folder || '';
                
                // 统计直属图片计数
                const imgFolderPath = folder ? `${root.rootId}/${folder}` : root.rootId;
                directCounts.set(imgFolderPath, (directCounts.get(imgFolderPath) || 0) + 1);
                
                if (!folder) continue;

                const parts = folder.split('/').filter(Boolean);
                let parentPath = root.rootId;
                let currentRelative = '';

                for (const part of parts) {
                    currentRelative = currentRelative ? `${currentRelative}/${part}` : part;
                    const currentPath = `${root.rootId}/${currentRelative}`;

                    if (!nodeMap.has(currentPath)) {
                        const node = {
                            name: part,
                            path: currentPath,
                            displayPath: currentPath,
                            expanded: false,
                            imageCount: 0,
                            isRoot: false,
                            children: []
                        };
                        nodeMap.set(currentPath, node);
                        const parentNode = nodeMap.get(parentPath);
                        if (parentNode) {
                            parentNode.children.push(node);
                        }
                    }

                    parentPath = currentPath;
                }
            }

            // ★ 第二遍：自底向上计算 imageCount（包含子文件夹）
            // 使用 BFS 反向：先收集所有节点按深度排序
            const allNodes = Array.from(nodeMap.values());
            // 按路径深度降序排列（最深的先处理）
            allNodes.sort((a, b) => {
                const depthA = (a.path.match(/\//g) || []).length;
                const depthB = (b.path.match(/\//g) || []).length;
                return depthB - depthA;
            });
            
            // 先赋值直接计数
            for (const node of allNodes) {
                node.imageCount = directCounts.get(node.path) || 0;
            }
            
            // 再自底向上累加子节点的计数到父节点
            for (const node of allNodes) {
                if (node.children && node.children.length > 0) {
                    for (const child of node.children) {
                        node.imageCount += child.imageCount;
                    }
                }
            }

            sortFolderNodes(rootNode.children);
            roots.push(rootNode);
        }

        return roots;
    }

    // ==================== 渐进式渲染 ====================

    /**
     * 渐进式渲染：将大量图片分批次渲染，避免卡顿
     * 每帧渲染一批，通过 requestAnimationFrame 调度
     * @param {Array} displayImages - 要渲染的图片数组
     * @param {Object} options - 配置项
     * @param {number} options.batchSize - 每批渲染数量（默认 50）
     */
    function progressiveRender(displayImages, options = {}) {
        // 取消之前的渐进渲染
        cancelProgressiveRender();

        hideGalleryPlaceholder();

        const batchSize = options.batchSize || getOptimalBatchSize() * 5;  // 默认 50-100 张/帧

        isProgressiveRendering = true;
        progressiveRenderCancel = false;

        // 清空画廊
        galleryGrid.innerHTML = '';
        galleryGrid.className = 'gallery-grid';
        updateGridColumns();

        // 如果图片数量少，直接一次性渲染
        if (displayImages.length <= batchSize * 2) {
            for (const img of displayImages) {
                galleryGrid.appendChild(createImageCard(img));
            }
            isProgressiveRendering = false;
            updateImageCount();
            return;
        }

        let currentIndex = 0;

        function renderBatch() {
            if (progressiveRenderCancel) {
                isProgressiveRendering = false;
                return;
            }

            const end = Math.min(currentIndex + batchSize, displayImages.length);
            const fragment = document.createDocumentFragment();
            for (let i = currentIndex; i < end; i++) {
                fragment.appendChild(createImageCard(displayImages[i]));
            }
            galleryGrid.appendChild(fragment);

            currentIndex = end;

            if (currentIndex < displayImages.length) {
                progressiveRenderTimer = requestAnimationFrame(renderBatch);
            } else {
                isProgressiveRendering = false;
                updateImageCount();
            }
        }

        progressiveRenderTimer = requestAnimationFrame(renderBatch);
    }

    function cancelProgressiveRender() {
        if (progressiveRenderTimer) {
            cancelAnimationFrame(progressiveRenderTimer);
            progressiveRenderTimer = null;
        }
        progressiveRenderCancel = true;
        isProgressiveRendering = false;
    }

    function isProgressivelyRendering() {
        return isProgressiveRendering;
    }

    // ==================== 过滤 ====================

    function applyCurrentFilter(options = {}) {
        const includeDescendants = options.includeDescendants !== false;

        if (!currentFolderFilter) {
            // ★ 无过滤时直接引用原数组（避免复制）
            filteredImages = images;
            return;
        }

        // ★ 规范化过滤路径（只计算一次）
        const normalizedFilter = currentFolderFilter.replace(/\\/g, '/');
        const filterPrefix = normalizedFilter + '/';
        
        // ★ 直接过滤，不先复制整个数组
        const len = images.length;
        const result = [];
        
        for (let i = 0; i < len; i++) {
            const img = images[i];
            const imgRootId = (img.rootId || img.rootPath || '').replace(/\\/g, '/');
            const imgFolder = (img.folder || '').replace(/\\/g, '/');
            const imageFolderPath = imgFolder ? `${imgRootId}/${imgFolder}` : imgRootId;

            if (includeDescendants) {
                if (imageFolderPath === normalizedFilter ||
                    imageFolderPath.startsWith(filterPrefix)) {
                    result.push(img);
                }
            } else {
                if (imageFolderPath === normalizedFilter) {
                    result.push(img);
                }
            }
        }

        filteredImages = result;
    }

    /**
     * ★ 在标签/收藏视图中执行批量操作后，重新过滤视图以实时反映变化
     */
    async function refreshCurrentFilteredView() {
        if (currentTagFilter) {
            const taggedPaths = await Storage.getImagesForTag(currentTagFilter);
            const pathSet = new Set(taggedPaths);
            filteredImages = images.filter(img => pathSet.has(img.path));
            sortImages();
            if (filteredImages.length > 100) {
                if (currentLayout === 'masonry') {
                    renderMasonry(filteredImages);
                } else if (currentLayout === 'pinterest') {
                    renderPinterest(filteredImages);
                } else if (currentLayout === 'list') {
                    renderList(filteredImages);
                } else {
                    progressiveRender(filteredImages);
                }
            } else {
                render();
            }
        } else if (currentFavoriteFilter) {
            const favPaths = await Storage.getAllFavorites();
            const pathSet = new Set(favPaths);
            filteredImages = images.filter(img => pathSet.has(img.path));
            sortImages();
            if (filteredImages.length > 100) {
                if (currentLayout === 'masonry') {
                    renderMasonry(filteredImages);
                } else if (currentLayout === 'pinterest') {
                    renderPinterest(filteredImages);
                } else if (currentLayout === 'list') {
                    renderList(filteredImages);
                } else {
                    progressiveRender(filteredImages);
                }
            } else {
                render();
            }
        }
    }

    /**
     * 判断 folderPath 是否为后端注册的真实路径
     * 通过 importedRoots 中的 rootId 匹配，而非路径格式判断
     */
    async function isServerRegisteredPath(folderPath) {
        if (!folderPath) return false;
        const normalized = folderPath.replace(/\\/g, '/');
        try {
            const roots = await Storage.getRegisteredRoots();
            return (roots || []).some(r => {
                const rootId = r.replace(/\\/g, '/');
                return rootId === normalized || normalized.startsWith(rootId + '/');
            });
        } catch (err) {
            console.warn('[isServerRegisteredPath] 查询失败:', err.message);
            return false;
        }
    }

    /**
     * ★ 问题一修复：中断前一个文件夹切换请求
     */
    // ==================== 文件夹分页增量加载 ====================

    // ★ 节流时间戳，防止 scroll 事件连续触发多次加载
    let _lastScrollCheckTime = 0;

    function checkLoadMoreOnScroll() {
        if (!isFilteringActive || !currentFolderFilter) return;
        if (isLoadingMoreFolder) return;
        if (folderLoadTotal > 0 && folderLoadOffset >= folderLoadTotal) return;

        // ★ 节流：同一帧内最多触发一次
        const now = Date.now();
        if (now - _lastScrollCheckTime < 200) return;
        _lastScrollCheckTime = now;

        // ★ 计算当前视窗底部对应的图片索引（视窗内最后一张的索引）
        let viewportLastIndex;
        if (currentLayout === 'masonry') {
            // 瀑布流：找到视窗底部所在行的最后一张
            const viewBottom = galleryScroll.scrollTop + galleryScroll.clientHeight;
            const rowH = thumbnailSize + 8;
            const lastVisibleRow = Math.floor(viewBottom / rowH);
            const lastItem = masonryLayout.findLast ?
                masonryLayout.findLast(item => item.row <= lastVisibleRow) :
                [...masonryLayout].reverse().find(item => item.row <= lastVisibleRow);
            viewportLastIndex = lastItem ? lastItem.imgIndex : 0;
        } else if (currentLayout === 'pinterest') {
            // 竖版瀑布流：用 y 坐标估算视窗底部图片索引
            const viewBottom = galleryScroll.scrollTop + galleryScroll.clientHeight;
            const lastItem = pinterestLayout.length > 0 ?
                pinterestLayout.reduce((best, item) => {
                    if (item.y <= viewBottom && item.imgIndex > best) return item.imgIndex;
                    return best;
                }, 0) : 0;
            viewportLastIndex = lastItem;
        } else if (currentLayout === 'list') {
            // 列表：两列，每行 2 张图
            const rowStep = thumbnailSize + 8;
            const viewBottom = galleryScroll.scrollTop + galleryScroll.clientHeight;
            const lastVisibleRow = Math.floor(viewBottom / rowStep);
            viewportLastIndex = Math.min(
                Math.max(0, (lastVisibleRow + 1) * 2 - 1),
                Math.max(0, filteredImages.length - 1)
            );
        } else {
            // 网格：行高固定，直接算索引
            const columns = getColumnCount();
            const rowHeight = thumbnailSize + 36 + 8;
            const viewBottom = galleryScroll.scrollTop + galleryScroll.clientHeight;
            const lastVisibleRow = Math.floor(viewBottom / rowHeight);
            viewportLastIndex = Math.min((lastVisibleRow + 1) * columns - 1, filteredImages.length - 1);
        }

        // ★ 视窗后方已加载的张数 = 已加载总数 - 视窗底部索引 - 1
        const imagesAheadOfViewport = filteredImages.length - viewportLastIndex - 1;

        // ★ 视窗后方不足 FOLDER_LOOKAHEAD 张时触发加载
        if (imagesAheadOfViewport < FOLDER_LOOKAHEAD) {
            _abortOutOfViewportLoads();
            loadMoreFolderImages();
        }
    }

    async function loadMoreFolderImages() {
        if (!currentFolderFilter || isLoadingMoreFolder) return;
        isLoadingMoreFolder = true;

        try {
            const folderPath = currentFolderFilter;

            // ★ 计算视窗后方已有多少张，再决定本次拉取数量（补足到 FOLDER_LOOKAHEAD）
            let viewportLastIndex = 0;
            if (currentLayout === 'masonry') {
                const viewBottom = galleryScroll.scrollTop + galleryScroll.clientHeight;
                const rowH = thumbnailSize + 8;
                const lastVisibleRow = Math.floor(viewBottom / rowH);
                const lastItem = masonryLayout.findLast ?
                    masonryLayout.findLast(item => item.row <= lastVisibleRow) :
                    [...masonryLayout].reverse().find(item => item.row <= lastVisibleRow);
                viewportLastIndex = lastItem ? lastItem.imgIndex : 0;
            } else if (currentLayout === 'pinterest') {
                const viewBottom = galleryScroll.scrollTop + galleryScroll.clientHeight;
                viewportLastIndex = pinterestLayout.length > 0 ?
                    pinterestLayout.reduce((best, item) => {
                        if (item.y <= viewBottom && item.imgIndex > best) return item.imgIndex;
                        return best;
                    }, 0) : 0;
            } else if (currentLayout === 'list') {
                const rowStep = thumbnailSize + 8;
                const viewBottom = galleryScroll.scrollTop + galleryScroll.clientHeight;
                const lastVisibleRow = Math.floor(viewBottom / rowStep);
                viewportLastIndex = Math.min(
                    Math.max(0, (lastVisibleRow + 1) * 2 - 1),
                    Math.max(0, filteredImages.length - 1)
                );
            } else {
                const columns = getColumnCount();
                const rowHeight = thumbnailSize + 36 + 8;
                const viewBottom = galleryScroll.scrollTop + galleryScroll.clientHeight;
                const lastVisibleRow = Math.floor(viewBottom / rowHeight);
                viewportLastIndex = Math.min((lastVisibleRow + 1) * columns - 1, filteredImages.length - 1);
            }
            const imagesAheadOfViewport = Math.max(0, filteredImages.length - viewportLastIndex - 1);
            const fetchCount = Math.max(FOLDER_LOOKAHEAD - imagesAheadOfViewport, FOLDER_LOOKAHEAD);

            const result = await loadImagesFromServer(folderPath, folderLoadOffset, fetchCount);

            if (result.images.length === 0) {
                folderLoadTotal = folderLoadOffset;
                return;
            }

            // ★ 记录追加前的 filteredImages 长度，以便精确取出新增部分
            const prevFilteredLength = filteredImages.length;

            images.push(...result.images);
            folderLoadTotal = result.total;
            folderLoadOffset += result.images.length;

            const normFolder = (folderPath || '').replace(/\\/g, '/');
            folderCacheMeta[normFolder] = { total: result.total };

            invalidatePathIndex();
            applyCurrentFilter();
            updateImageCount();

            if (currentLayout === 'masonry') {
                // 瀑布流：绝对定位追加，不影响已有卡片，安全
                const newFiltered = filteredImages.slice(prevFilteredLength);
                if (newFiltered.length > 0) appendImageCards(newFiltered);
            } else if (currentLayout === 'pinterest') {
                const newFiltered = filteredImages.slice(prevFilteredLength);
                if (newFiltered.length > 0) appendImageCards(newFiltered);
            } else if (currentLayout === 'list') {
                const newFiltered = filteredImages.slice(prevFilteredLength);
                if (newFiltered.length > 0) appendImageCards(newFiltered);
            } else {
                // ★ Grid 模式：只更新 spacer-bottom 高度，绝对不动 scrollTop 和已有卡片
                //   不重置 renderedRange，避免触发全量重建 → scrollTop 被浏览器回弹 → 再次触发加载的死循环
                const columns = getColumnCount();
                const rowHeight = (thumbnailSize + 36) + 8;
                const totalItems = filteredImages.length;
                const endIndex = renderedRange.end > 0 ? renderedRange.end : 0;
                const remainingRows = Math.ceil(Math.max(0, totalItems - endIndex) / columns);
                const spacerBottom = galleryGrid.querySelector('.vs-spacer-bottom');
                if (spacerBottom) {
                    spacerBottom.style.height = `${Math.max(0, remainingRows * rowHeight)}px`;
                } else {
                    // spacerBottom 不存在（首次或全量渲染未完成），才回退到失效重建
                    renderedRange = { start: -1, end: -1 };
                }
                // ★ 立即预热新加载图片的缩略图，避免用户滚动到这些卡片时看到黑框
                _prewarmThumbnails(filteredImages, 0, prevFilteredLength);
            }

        } catch (err) {
            console.warn('[Gallery] 增量加载文件夹图片失败:', err.message);
        } finally {
            isLoadingMoreFolder = false;
            // 重置节流时间戳，允许下次合法触发
            _lastScrollCheckTime = 0;
        }
    }

    function abortFolderSwitch() {
        if (folderAbortController) {
            folderAbortController.abort();
            folderAbortController = null;
        }
    }

    async function filterByFolder(folderPath, folderName, options = {}) {
        const includeDescendants = options.includeDescendants !== false;
        const forceRefresh = options.forceRefresh || false;

        // ★ 问题一修复：中断前一个文件夹切换请求
        abortFolderSwitch();
        // 取消正在进行的渐进渲染
        cancelProgressiveRender();

        // ★ 问题一修复：切换文件夹时强制重置渲染状态
        renderedRange = { start: -1, end: -1 };
        scrollTop = 0;
        if (galleryScroll) galleryScroll.scrollTop = 0;

        // ★ 修复：切换文件夹时主动同步 containerHeight，
        //    避免 containerHeight 仍为 0（从未滚动过）或过期值，
        //    导致 renderGrid 算出 rowsPerView=0，渲染 0 张卡片，
        //    表现为图廊空白直到用户滚动才刷新。
        if (galleryScroll) containerHeight = galleryScroll.clientHeight;

        currentFolderFilter = folderPath || null;
        currentTagFilter = null;
        currentFavoriteFilter = false;
        isFilteringActive = !!currentFolderFilter;

        // 重置分页加载状态
        folderLoadTotal = 0;
        folderLoadOffset = 0;
        isLoadingMoreFolder = false;

        // ★ 问题一修复：创建新的中断控制器
        folderAbortController = new AbortController();
        const signal = folderAbortController.signal;
        // ★ 修复：捕获当前控制器引用，用于 finally 块中正确判断
        const currentController = folderAbortController;

        // ★ 后端注册目录：通过 importedRoots 匹配判断，而非路径格式判断
        console.log('[filterByFolder] folderPath:', folderPath, 'importedRoots:', importedRoots.map(r => r.rootId));
        if (folderPath && await isServerRegisteredPath(folderPath)) {
            console.log('[filterByFolder] 进入服务端路径分支:', folderPath);
            showLoading(true);
            try {
                // ★ 问题一修复：先清空图廊，避免显示旧内容
                galleryGrid.innerHTML = '';
                galleryGrid.className = 'gallery-grid';

                const normalizedFolder = folderPath.replace(/\\/g, '/');

                // ★ 问题三修复：如果 forceRefresh，先调用后端重新扫描
                if (forceRefresh) {
                    try {
                        if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
                            // 找到该子文件夹所属的根目录
                            let rootPath = folderPath;
                            for (const root of importedRoots) {
                                const rootId = (root.rootId || '').replace(/\\/g, '/');
                                if (normalizedFolder === rootId || normalizedFolder.startsWith(rootId + '/')) {
                                    rootPath = root.rootId;
                                    break;
                                }
                            }
                            await WailsBridge.rescanFolder(rootPath);
                            console.log('[Gallery] 强制重新扫描完成:', rootPath);
                        }
                    } catch (err) {
                        console.warn('[Gallery] 强制重新扫描失败:', err.message);
                    }
                }

                // ★ 缓存优化：如果 images 中已有该文件夹的图片且非强制刷新，直接使用缓存
                const cachedImages = !forceRefresh ? images.filter(img => {
                    if (!img._fromServer) return false;
                    const imgRoot = (img.rootPath || '').replace(/\\/g, '/');
                    const imgFolder = (img.folder || '').replace(/\\/g, '/');
                    const imageFolderPath = imgFolder ? `${imgRoot}/${imgFolder}` : imgRoot;
                    return imageFolderPath === normalizedFolder || imageFolderPath.startsWith(normalizedFolder + '/');
                }) : [];

                if (cachedImages.length > 0 && folderCacheMeta[normalizedFolder]) {
                    // ★ 缓存命中：跳过服务端请求，直接用缓存图片渲染
                    folderLoadTotal = folderCacheMeta[normalizedFolder].total;
                    folderLoadOffset = cachedImages.length;
                    isLoadingMoreFolder = false;
                    console.log(`[Gallery] 缓存命中: ${normalizedFolder}，${cachedImages.length}/${folderLoadTotal} 张`);
                } else {
                    // ★ 缓存未命中（或强制刷新）：从后端加载
                    if (forceRefresh) {
                        delete folderCacheMeta[normalizedFolder];
                    }

                    console.log('[filterByFolder] 缓存未命中，调用 loadImagesFromServer:', normalizedFolder);
                    const firstResult = await loadImagesFromServer(folderPath, 0, FOLDER_LOOKAHEAD);
                    let serverImages = firstResult.images;
                    folderLoadTotal = firstResult.total;
                    console.log('[filterByFolder] loadImagesFromServer 返回:', serverImages.length, '张, total:', folderLoadTotal);
                    console.log('[filterByFolder] 第一张图片数据:', serverImages[0] ? JSON.stringify(serverImages[0]) : '无');
                    // ★ 使用实际返回数量，而非 FOLDER_LOOKAHEAD，避免跳过数据
                    folderLoadOffset = serverImages.length;
                    isLoadingMoreFolder = false;

                    // ★ 保存缓存元数据
                    folderCacheMeta[normalizedFolder] = { total: firstResult.total };

                    // ★ 后端返回 0 张图片时
                    if (serverImages.length === 0 && await isServerRegisteredPath(folderPath)) {
                        if (folderLoadTotal > 0) {
                            // total > 0 说明后端知道有图但暂未返回 → 监听 scan:complete 事件而非轮询
                            showScanningPlaceholder();
                            await new Promise(resolve => {
                                let resolved = false;
                                const finish = () => {
                                    if (resolved) return;
                                    resolved = true;
                                    if (window.runtime && window.runtime.EventsOff) {
                                        try { window.runtime.EventsOff('scan:complete'); } catch (e) {}
                                    }
                                    resolve();
                                };
                                if (window.runtime && window.runtime.EventsOn) {
                                    window.runtime.EventsOn('scan:complete', (data) => {
                                        const scanRoot = ((data && data.rootPath) || '').replace(/\\/g, '/');
                                        const fp = (folderPath || '').replace(/\\/g, '/');
                                        if (scanRoot === '' || fp === scanRoot || fp.startsWith(scanRoot + '/')) {
                                            finish();
                                        }
                                    });
                                }
                                // 兜底超时
                                setTimeout(finish, 20000);
                            });
                            if (signal.aborted) return;
                            const retryResult = await loadImagesFromServer(folderPath, 0, FOLDER_LOOKAHEAD);
                            serverImages = retryResult.images;
                            folderLoadTotal = retryResult.total;
                            folderLoadOffset = serverImages.length;
                            folderCacheMeta[normalizedFolder] = { total: retryResult.total };
                            if (serverImages.length === 0) {
                                showLoading(false);
                                galleryGrid.innerHTML = `
                                    <div class="folder-empty">
                                        <div class="folder-empty-icon"><span class="icon icon-warning"></span></div>
                                        <p class="folder-empty-text">图片加载超时</p>
                                        <p class="folder-empty-hint">请点击 <span class="icon icon-refresh"></span> 刷新按钮重试</p>
                                    </div>`;
                                galleryGrid.className = 'gallery-grid';
                                updateImageCount();
                                return;
                            }
                        }
                        // total === 0：后端无数据，不轮询；后台扫描完成后 scan:complete 事件会自动刷新
                    }

                    // ★ 问题一修复：检查是否已被中断
                    if (signal.aborted) {
                        console.log('[Gallery] 文件夹切换已中断');
                        return;
                    }

                    // 移除该 rootPath 旧的后端图片，避免重复
                    images = images.filter(img => {
                        if (!img._fromServer) return true;
                        const imgRoot = (img.rootPath || '').replace(/\\/g, '/');
                        const imgFolder = (img.folder || '').replace(/\\/g, '/');
                        const imageFolderPath = imgFolder ? `${imgRoot}/${imgFolder}` : imgRoot;
                        return !(imageFolderPath === normalizedFolder || imageFolderPath.startsWith(normalizedFolder + '/'));
                    });

                    if (serverImages.length > 0) {
                        images.push(...serverImages);
                    }

                    // ★ 修复：images 数组被过滤/追加后，使路径索引失效
                    invalidatePathIndex();
                }

                // ★ 问题一修复：再次检查是否已被中断
                if (signal.aborted) {
                    console.log('[Gallery] 文件夹切换已中断（合并后）');
                    return;
                }

                applyCurrentFilter({ includeDescendants });
                sortImages();
                // 标记加载完成，防止同一次 scan 的 complete 事件触发重复刷新
                if (Gallery._finishScanLoad) Gallery._finishScanLoad(currentFolderFilter);

                const displayImages = isFilteringActive ? filteredImages : images;
                if (displayImages.length > 100) {
                    if (currentLayout === 'masonry') {
                        renderMasonry(displayImages);
                    } else if (currentLayout === 'pinterest') {
                        renderPinterest(displayImages);
                    } else if (currentLayout === 'list') {
                        renderList(displayImages);
                    } else {
                        progressiveRender(displayImages);
                    }
                } else {
                    render();
                }

            } finally {
                showLoading(false);
                // ★ 修复：使用闭包捕获的 currentController 判断是否是当前请求
                if (folderAbortController === currentController) {
                    folderAbortController = null;
                }
            }

            clearSelection();
            return;
        }

        // 前端本地导入目录维持原逻辑
        applyCurrentFilter({ includeDescendants });
        sortImages();
        
        const displayImages = isFilteringActive ? filteredImages : images;
        await lazyLoadFolder(folderPath, displayImages);

        if (displayImages.length > 100) {
            if (currentLayout === 'masonry') {
                renderMasonry(displayImages);
            } else if (currentLayout === 'pinterest') {
                renderPinterest(displayImages);
            } else if (currentLayout === 'list') {
                renderList(displayImages);
            } else {
                progressiveRender(displayImages, {
                    folderPath: folderPath || ''
                });
            }
        } else {
            render();
        }
        
        clearSelection();
    }

    async function filterByTag(tagId) {
        currentTagFilter = tagId;
        currentFavoriteFilter = false;
        currentFolderFilter = null;
        isFilteringActive = true;

        // ★ 即时视觉反馈：清空格子 + 显示加载中
        showLoading(true);
        galleryGrid.innerHTML = '';
        galleryGrid.className = 'gallery-grid';
        scrollTop = 0;
        if (galleryScroll) galleryScroll.scrollTop = 0;
        renderedRange = { start: -1, end: -1 };

        try {
            const taggedPaths = await Storage.getImagesForTag(tagId);

            // ★ 文件夹标签：直接走文件夹过滤逻辑
            if (taggedPaths && taggedPaths.linkedFolder) {
                currentTagFilter = null;
                await filterByFolder(taggedPaths.linkedFolder, null, { forceRefresh: false });
                currentTagFilter = tagId;
                return;
            }

            if (taggedPaths.length === 0) {
                filteredImages = [];
                render();
                return;
            }

            // ★ 精准查询：只向 Go 请求标签关联的图片路径，而非全量
            const firstResult = await loadImagesByPaths(taggedPaths, 0, FOLDER_LOOKAHEAD);
            const tagImages = firstResult.images;

            // 移除已有 server 同路径旧条目，避免重复；保留非 server 图片
            const pathSet = new Set(taggedPaths);
            images = images.filter(img => !(img._fromServer && pathSet.has(img.path)));

            if (tagImages.length > 0) {
                images.push(...tagImages);
            }
            invalidatePathIndex();

            filteredImages = images.filter(img => pathSet.has(img.path));
            sortImages();

            // 分页加载大量标签图片时，用渐进式渲染避免卡顿
            if (filteredImages.length > 100) {
                progressiveRender(filteredImages, { folderPath: '' });
            } else {
                render();
            }

            clearSelection();
        } catch (err) {
            console.error('[Gallery] 标签过滤失败:', err);
            render();
        } finally {
            showLoading(false);
        }
    }

    async function filterByFavorites(onlyFavorites) {
        if (!onlyFavorites) {
            currentTagFilter = null;
            currentFavoriteFilter = false;
            isFilteringActive = !!currentFolderFilter;
            applyCurrentFilter();
            sortImages();
            render();
            return;
        }
        currentTagFilter = null;
        currentFavoriteFilter = true;
        currentFolderFilter = null;
        isFilteringActive = true;

        // ★ 即时视觉反馈
        showLoading(true);
        galleryGrid.innerHTML = '';
        galleryGrid.className = 'gallery-grid';
        scrollTop = 0;
        if (galleryScroll) galleryScroll.scrollTop = 0;
        renderedRange = { start: -1, end: -1 };

        try {
            const favPaths = await Storage.getAllFavorites();

            if (favPaths.length === 0) {
                filteredImages = [];
                render();
                return;
            }

            // ★ 精准查询：只向 Go 请求收藏的图片路径
            const firstResult = await loadImagesByPaths(favPaths, 0, FOLDER_LOOKAHEAD);
            const favImages = firstResult.images;

            const pathSet = new Set(favPaths);
            images = images.filter(img => !(img._fromServer && pathSet.has(img.path)));

            if (favImages.length > 0) {
                images.push(...favImages);
            }
            invalidatePathIndex();

            filteredImages = images.filter(img => pathSet.has(img.path));
            sortImages();

            if (filteredImages.length > 100) {
                progressiveRender(filteredImages, { folderPath: '' });
            } else {
                render();
            }

            clearSelection();
        } catch (err) {
            console.error('[Gallery] 收藏过滤失败:', err);
            render();
        } finally {
            showLoading(false);
        }
    }

    async function clearFilters() {
        currentFolderFilter = null;
        currentTagFilter = null;
        currentFavoriteFilter = false;
        isFilteringActive = false;
        // ★ 不复制数组，直接引用
        filteredImages = images;
        sortImages();
        
        // 如果没有任何文件夹被加载过，不自动加载全部
        const hasAnyLoaded = images.some(img => img._loaded);
        if (!hasAnyLoaded) {
            galleryGrid.innerHTML = '';
            galleryGrid.className = 'gallery-grid';
            showGalleryPlaceholder('请点击左侧文件夹来浏览图片');
            updateImageCount();
            return;
        }
        
        render();
    }

    // ==================== 排序 ====================

    function sortImages() {
        // ★ 就地排序，避免复制整个数组
        const arr = isFilteringActive ? filteredImages : images;

        switch (sortOrder) {
            case 'name-asc':
                arr.sort((a, b) => a.name.localeCompare(b.name));
                break;
            case 'name-desc':
                arr.sort((a, b) => b.name.localeCompare(a.name));
                break;
            case 'date-desc':
                arr.sort((a, b) => (b.lastModified || 0) - (a.lastModified || 0));
                break;
            case 'date-asc':
                arr.sort((a, b) => (a.lastModified || 0) - (b.lastModified || 0));
                break;
            case 'size-desc':
                arr.sort((a, b) => (b.size || 0) - (a.size || 0));
                break;
            case 'size-asc':
                arr.sort((a, b) => (a.size || 0) - (b.size || 0));
                break;
        }

        // ★ 排序后路径索引失效
        invalidatePathIndex();
        // ★ 排序后渲染范围失效，强制虚拟滚动下一次重新计算
        renderedRange = { start: -1, end: -1 };
    }

    // ==================== 渲染 ====================

    /**
     * ★ 扫描中占位：在画廊中央显示"正在扫描文件夹..."，替代"没有图片"
     */
    function showScanningPlaceholder() {
        hideGalleryPlaceholder();
        galleryGrid.innerHTML = `
            <div class="folder-empty">
                <div class="folder-empty-icon">⏳</div>
                <p class="folder-empty-text">${I18n.t("gallery.folder_empty")}</p>
                <p class="folder-empty-hint">${I18n.t("gallery.folder_empty_hint")}</p>
            </div>`;
        galleryGrid.className = 'gallery-grid';
        updateImageCount();
    }

    function escapeHtml(str) {
        const div = document.createElement('div');
        div.textContent = str;
        return div.innerHTML;
    }

    function render() {
        const displayImages = isFilteringActive ? filteredImages : images;
        console.log('[render] 被调用，displayImages.length:', displayImages.length, 'isFilteringActive:', isFilteringActive);

        // 渲染前刷新收藏缓存（异步，不阻塞渲染）
        if (favoriteCacheDirty) {
            refreshFavoriteCache();
        }

        if (displayImages.length === 0) {
            if (currentFolderFilter) {
                hideGalleryPlaceholder();
                galleryGrid.innerHTML = `
                    <div class="folder-empty">
                        <div class="folder-empty-icon"><span class="icon icon-folder"></span></div>
                        <p class="folder-empty-text" data-i18n="gallery.folder_empty">图片可能加载中，请稍等</p>
                        <p class="folder-empty-hint" data-i18n="gallery.folder_empty_hint">图片较多或硬盘较慢时可能需要一些时间</p>
                    </div>`;
            } else {
                galleryGrid.innerHTML = '';
                showGalleryPlaceholder(I18n.t('gallery.no_images'));
            }
            galleryGrid.className = 'gallery-grid';
            updateImageCount();
            return;
        }

        if (currentLayout === 'masonry') {
            renderMasonry(displayImages);
        } else if (currentLayout === 'pinterest') {
            renderPinterest(displayImages);
        } else if (currentLayout === 'list') {
            renderList(displayImages);
        } else {
            renderGrid(displayImages);
        }

        updateImageCount();
    }

    function showGalleryPlaceholder(text) {
        const placeholder = document.getElementById('galleryPlaceholder');
        if (placeholder) {
            placeholder.textContent = text;
            placeholder.style.display = '';
        }
    }

    function hideGalleryPlaceholder() {
        const placeholder = document.getElementById('galleryPlaceholder');
        if (placeholder) {
            placeholder.style.display = 'none';
        }
    }

    function renderGrid(displayImages) {
        // ★ 修复 Bug 2：清除瀑布流留下的内联样式残留
        //   renderMasonry 写入 height/position，切换到网格时必须重置
        galleryGrid.style.height = '';
        galleryGrid.style.minHeight = '';
        galleryGrid.style.position = '';

        // ★ 修复 Bug 1：首次渲染时 containerHeight 可能为 0（用户从未滚动过），
        //   导致 rowsPerView 计算错误、spacer 高度错位、checkLoadMoreOnScroll 提前停止
        if (galleryScroll) containerHeight = galleryScroll.clientHeight;

        hideGalleryPlaceholder();
        galleryGrid.className = 'gallery-grid';
        updateGridColumns();

        const totalItems = displayImages.length;
        const columns = getColumnCount();
        const cardHeight = thumbnailSize + 36;
        const gap = 8;
        const rowHeight = cardHeight + gap;
        const rowsPerView = Math.ceil(containerHeight / rowHeight) + OVERSCAN;
        const startRow = Math.max(0, Math.floor(scrollTop / rowHeight) - OVERSCAN);
        const startIndex = startRow * columns;
        // ★ 底部缓冲与顶部对称：顶部有 OVERSCAN 行保护，底部也需要足够的行数，
        //   否则往上滚动时被删除的卡片距离视窗底边太近，用户会看到最后一行消失。
        const BOTTOM_PAD = Math.max(6, Math.floor(OVERSCAN / 4));
        const endIndex = Math.min(totalItems, startIndex + rowsPerView * columns + columns * BOTTOM_PAD);

        // ★ 滞回：仅在索引变化微小（≤1 行）且实际滚动距离也很小（<1.5 行高）时跳过，
        //   防止行边界振荡导致最后一排反复销毁重建。大步长滚动始终放行。
        //   renderedRange.start >= 0 确保 renderedRange 未被外部重置（如布局切换）
        if (_lastRenderScrollTop >= 0 && renderedRange.start >= 0) {
            const indexDelta = Math.abs(startIndex - renderedRange.start);
            const scrollDelta = Math.abs(scrollTop - _lastRenderScrollTop);
            if (indexDelta <= columns && scrollDelta < rowHeight * 1.5) {
                return;
            }
        }

        // ★ 相同窗口且 DOM 已存在 → 跳过渲染，避免跳动
        if (renderedRange.start === startIndex && renderedRange.end === endIndex && galleryGrid.children.length > 2) {
            return;
        }

        _lastRenderScrollTop = scrollTop;

        const prevStart = renderedRange.start;
        const prevEnd = renderedRange.end;
        renderedRange = { start: startIndex, end: endIndex };

        // ★ 检测 DOM 结构是否来自其他渲染器（瀑布流 / progressiveRender），
        //   这类 DOM 没有 vs-spacer，增量更新逻辑不兼容，必须全量重建
        const hasGridSpacers = galleryGrid.querySelector('.vs-spacer-top');
        const domNeedsRebuild = galleryGrid.children.length > 0 && !hasGridSpacers;

        // ★ 首次渲染（或全量刷新）：直接构建
        const isFirstRender = domNeedsRebuild || prevStart < 0 || galleryGrid.children.length <= 2;
        if (isFirstRender) {
            const fragment = document.createDocumentFragment();

            const spacerTop = document.createElement('div');
            spacerTop.className = 'vs-spacer-top';
            spacerTop.style.cssText = `grid-column:1/-1;height:${startRow * rowHeight}px`;
            fragment.appendChild(spacerTop);

            for (let i = startIndex; i < endIndex && i < totalItems; i++) {
                fragment.appendChild(createImageCard(displayImages[i]));
            }

            const remainingRows = Math.ceil((totalItems - endIndex) / columns);
            const spacerBottom = document.createElement('div');
            spacerBottom.className = 'vs-spacer-bottom';
            spacerBottom.style.cssText = `grid-column:1/-1;height:${Math.max(0, remainingRows * rowHeight)}px`;
            fragment.appendChild(spacerBottom);

            galleryGrid.innerHTML = '';
            galleryGrid.appendChild(fragment);
            _prewarmThumbnails(displayImages, startIndex, endIndex);
            return;
        }

        // ★ 差异更新：只增删滑入/滑出视窗的卡片，不碰视窗内已有卡片
        //   这样图片不会因重建而跳动，也不会触发重新加载

        // 1. 更新 spacer-top 高度
        const spacerTop = galleryGrid.querySelector('.vs-spacer-top');
        if (spacerTop) {
            spacerTop.style.height = `${startRow * rowHeight}px`;
        }

        // 2. 更新 spacer-bottom 高度
        const remainingRows = Math.ceil((totalItems - endIndex) / columns);
        const spacerBottom = galleryGrid.querySelector('.vs-spacer-bottom');
        if (spacerBottom) {
            spacerBottom.style.height = `${Math.max(0, remainingRows * rowHeight)}px`;
        }

        // 3. 删除滑出视窗顶部的卡片（index < startIndex）
        //    卡片紧跟在 spacerTop 之后，按顺序删除头部
        if (prevStart < startIndex) {
            const toRemoveCount = Math.min(startIndex - prevStart, prevEnd - prevStart);
            for (let i = 0; i < toRemoveCount; i++) {
                // spacerTop 之后第一个卡片
                const afterSpacer = spacerTop ? spacerTop.nextSibling : galleryGrid.firstChild;
                if (afterSpacer && afterSpacer !== spacerBottom) {
                    galleryGrid.removeChild(afterSpacer);
                }
            }
        }

        // 4. 删除滑出视窗底部的卡片（index >= endIndex）
        //    卡片在 spacerBottom 之前，按顺序删除尾部
        if (prevEnd > endIndex) {
            const toRemoveCount = Math.min(prevEnd - endIndex, prevEnd - Math.max(prevStart, startIndex));
            for (let i = 0; i < toRemoveCount; i++) {
                const beforeSpacer = spacerBottom ? spacerBottom.previousSibling : galleryGrid.lastChild;
                if (beforeSpacer && beforeSpacer !== spacerTop) {
                    galleryGrid.removeChild(beforeSpacer);
                }
            }
        }

        // 5. 在顶部前插入新滑入的卡片（新 startIndex < 旧 startIndex）
        if (startIndex < prevStart) {
            const insertBefore = spacerTop ? spacerTop.nextSibling : galleryGrid.firstChild;
            const fragment = document.createDocumentFragment();
            const insertEnd = Math.min(prevStart, endIndex);
            for (let i = startIndex; i < insertEnd && i < totalItems; i++) {
                fragment.appendChild(createImageCard(displayImages[i]));
            }
            galleryGrid.insertBefore(fragment, insertBefore);
        }

        // 6. 在底部追加新滑入的卡片（新 endIndex > 旧 endIndex）
        if (endIndex > prevEnd) {
            const fragment = document.createDocumentFragment();
            const appendStart = Math.max(prevEnd, startIndex);
            for (let i = appendStart; i < endIndex && i < totalItems; i++) {
                fragment.appendChild(createImageCard(displayImages[i]));
            }
            // 插入到 spacerBottom 之前
            if (spacerBottom) {
                galleryGrid.insertBefore(fragment, spacerBottom);
            } else {
                galleryGrid.appendChild(fragment);
            }
        }

        _prewarmThumbnails(displayImages, startIndex, endIndex);
    }

    function computeMasonryLayout(displayImages) {
        const gap = 8;
        const padding = 24; // galleryScroll padding (12px × 2)
        const containerW = Math.max(200, (galleryScroll ? galleryScroll.clientWidth : 800) - padding);
        const rowH = thumbnailSize;
        masonryRowHeight = rowH;
        masonryContainerWidth = containerW;
        masonryFirstId = displayImages.length > 0 ? (displayImages[0].id || '') : '';
        masonryLayout = [];
        masonryTotalHeight = 0;

        if (displayImages.length === 0) {
            masonryLayoutVersion++;
            return;
        }

        let currentX = 0;
        let row = 0;

        for (let i = 0; i < displayImages.length; i++) {
            const img = displayImages[i];
            // ★ 默认宽高比：未知尺寸的图片用 4:3 替代正方形，更接近真实照片比例
            const w = img.width || (img.height ? Math.round(img.height * 4/3) : 0);
            const h = img.height || (img.width ? Math.round(img.width * 3/4) : 0);
            const ratio = (w && h && h > 0) ? w / h : 4/3;
            const displayW = Math.round(Math.min(rowH * ratio, containerW));

            if (currentX > 0 && currentX + displayW + gap > containerW) {
                row++;
                currentX = 0;
            }

            masonryLayout.push({
                imgIndex: i,
                row: row,
                x: currentX,
                y: row * (rowH + gap),
                w: displayW,
                h: rowH
            });

            currentX += displayW + gap;
        }

        // ★ 修复：最后一行不加 gap，总高度精确等于最后一行底边
        masonryTotalHeight = (row + 1) * rowH + row * gap;
        masonryLayoutVersion++;
    }

    function renderMasonry(displayImages) {
        hideGalleryPlaceholder();
        galleryGrid.className = 'gallery-grid masonry';
        galleryGrid.innerHTML = '';

        const rowH = thumbnailSize;
        const gap = 8;
        const rowStep = rowH + gap;

        // 布局缓存失效则重新计算
        const firstId = displayImages.length > 0 ? (displayImages[0].id || '') : '';
        if (masonryLayout.length !== displayImages.length ||
            masonryFirstId !== firstId ||
            masonryRowHeight !== rowH ||
            masonryContainerWidth !== (galleryScroll ? galleryScroll.clientWidth - 24 : 0) ||
            masonryLayoutVersion === 0) {
            computeMasonryLayout(displayImages);
        }

        if (displayImages.length === 0) {
            updateImageCount();
            return;
        }

        const totalRows = masonryLayout.length > 0
            ? masonryLayout[masonryLayout.length - 1].row + 1
            : 0;

        // ★ 修复：用 height 而非 minHeight 撑开滚动条
        //   minHeight 不能作为 position:absolute 子元素的定位基准
        galleryGrid.style.height = masonryTotalHeight + 'px';
        galleryGrid.style.minHeight = '';

        // ★ 修复：containerHeight 可能在首次渲染时为 0，fallback 到 clientHeight
        const viewH = containerHeight || galleryScroll.clientHeight;

        // 计算可见行范围
        const firstVisibleRow = Math.max(0, Math.floor(scrollTop / rowStep) - OVERSCAN);
        const lastVisibleRow = Math.min(
            totalRows - 1,
            Math.ceil((scrollTop + viewH) / rowStep) + OVERSCAN
        );

        // 筛选可见图片
        const visibleItems = masonryLayout.filter(
            item => item.row >= firstVisibleRow && item.row <= lastVisibleRow
        );

        const fragment = document.createDocumentFragment();

        // 创建可见卡片（spacer 已由父容器 height 撑开，不再需要绝对定位占位块）
        for (const item of visibleItems) {
            if (item.imgIndex < displayImages.length) {
                fragment.appendChild(createImageCard(displayImages[item.imgIndex], item));
            }
        }

        galleryGrid.appendChild(fragment);
        updateImageCount();

        // 预温渲染范围外的缩略图
        const maxVisibleIndex = visibleItems.length > 0
            ? Math.max(...visibleItems.map(v => v.imgIndex))
            : 0;
        _prewarmThumbnails(displayImages, 0, maxVisibleIndex + 1);
    }

    // ============================================================
    //  竖版瀑布流（Pinterest 风格）
    //  固定列宽，不定高度，图片保留原始比例，不裁剪
    // ============================================================

    function computePinterestLayout(displayImages) {
        const gap = 8;
        const padding = 24; // galleryScroll padding (12px × 2)
        const containerW = Math.max(200, (galleryScroll ? galleryScroll.clientWidth : 800) - padding);
        // ★ 用 thumbnailSize 控制列宽（与 masonry 用 thumbnailSize 控制行高一致）
        //    列宽 = thumbnailSize，最少 2 列（确保竖版瀑布流效果）
        const colW = Math.max(80, Math.min(thumbnailSize, containerW - gap));
        const colCount = Math.max(2, Math.floor((containerW + gap) / (colW + gap)));

        pinterestColCount = colCount;
        pinterestColWidth = colW;
        pinterestContainerWidth = containerW;
        pinterestFirstId = displayImages.length > 0 ? (displayImages[0].id || '') : '';
        pinterestLayout = [];
        pinterestTotalHeight = 0;

        if (displayImages.length === 0) {
            pinterestLayoutVersion++;
            return;
        }

        // 每列的当前底部 Y 坐标
        const colHeights = new Array(colCount).fill(0);

        for (let i = 0; i < displayImages.length; i++) {
            const img = displayImages[i];
            const w = img.width || (img.height ? Math.round(img.height * 4/3) : 0);
            const h = img.height || (img.width ? Math.round(img.width * 3/4) : 0);
            const ratio = (w && h && h > 0) ? w / h : 4/3;

            // 选择当前最短的列
            let shortestCol = 0;
            let shortestHeight = colHeights[0];
            for (let c = 1; c < colCount; c++) {
                if (colHeights[c] < shortestHeight) {
                    shortestCol = c;
                    shortestHeight = colHeights[c];
                }
            }

            const displayH = Math.round(colW / ratio);
            const x = shortestCol * (colW + gap);
            const y = shortestHeight;

            pinterestLayout.push({
                imgIndex: i,
                col: shortestCol,
                x: x,
                y: y,
                w: colW,
                h: displayH
            });

            colHeights[shortestCol] = y + displayH + gap;
        }

        pinterestTotalHeight = Math.max(...colHeights) - gap;
        pinterestLayoutVersion++;
    }

    function renderPinterest(displayImages) {
        hideGalleryPlaceholder();
        galleryGrid.className = 'gallery-grid pinterest';
        galleryGrid.innerHTML = '';

        const gap = 8;

        // 布局缓存失效则重新计算
        const firstId = displayImages.length > 0 ? (displayImages[0].id || '') : '';
        if (pinterestLayout.length !== displayImages.length ||
            pinterestFirstId !== firstId ||
            pinterestColWidth === 0 ||
            pinterestContainerWidth !== (galleryScroll ? galleryScroll.clientWidth - 24 : 0) ||
            pinterestLayoutVersion === 0) {
            computePinterestLayout(displayImages);
        }

        if (displayImages.length === 0) {
            updateImageCount();
            return;
        }

        galleryGrid.style.height = pinterestTotalHeight + 'px';
        galleryGrid.style.minHeight = '';

        const viewH = containerHeight || galleryScroll.clientHeight;
        const colW = pinterestColWidth;
        // 估算可见行的行高范围，用平均高度做近似
        const avgItemH = pinterestLayout.length > 0
            ? pinterestTotalHeight / Math.max(1, pinterestLayout.length / pinterestColCount)
            : 200;
        const avgFullRowH = avgItemH + gap; // 一个"列满行"的平均高度

        const firstVisibleY = Math.max(0, scrollTop - avgFullRowH * OVERSCAN);
        const lastVisibleY = scrollTop + viewH + avgFullRowH * OVERSCAN;

        // 筛选可见范围内的图片（用每张图实际 y 坐标判断）
        const visibleItems = pinterestLayout.filter(
            item => (item.y + item.h + gap) >= firstVisibleY && item.y <= lastVisibleY
        );

        const fragment = document.createDocumentFragment();
        for (const item of visibleItems) {
            if (item.imgIndex < displayImages.length) {
                fragment.appendChild(createImageCard(displayImages[item.imgIndex], { layout: 'pinterest', item: item }));
            }
        }

        galleryGrid.appendChild(fragment);
        updateImageCount();

        const maxVisibleIndex2 = visibleItems.length > 0
            ? Math.max(...visibleItems.map(v => v.imgIndex))
            : 0;
        _prewarmThumbnails(displayImages, 0, maxVisibleIndex2 + 1);
    }

    // ============================================================
    //  列表模式：大缩略图 + 详细信息（文件名、尺寸、大小、格式、时间、文件夹）
    // ============================================================

    function renderList(displayImages) {
        hideGalleryPlaceholder();
        galleryGrid.className = 'gallery-grid list';
        galleryGrid.innerHTML = '';

        const gap = 8;
        const padding = 24;
        const containerW = Math.max(300, (galleryScroll ? galleryScroll.clientWidth : 800) - padding);
        const colW = Math.floor((containerW - gap) / 2); // 两列，每列宽度
        const rowH = Math.max(60, thumbnailSize); // 行高 = 缩略图大小

        listRowHeight = rowH;
        listContainerWidth = containerW;
        const firstId = displayImages.length > 0 ? (displayImages[0].id || '') : '';
        const needsRecompute = listFirstId !== firstId ||
            listRowHeight !== rowH ||
            listContainerWidth !== containerW ||
            listLayoutVersion === 0;

        if (needsRecompute) {
            listFirstId = firstId;
            listRowHeight = rowH;
            listContainerWidth = containerW;
            const totalRows = Math.ceil(displayImages.length / 2);
            listTotalHeight = totalRows * (rowH + gap);
            listLayoutVersion++;
        }

        if (displayImages.length === 0) {
            updateImageCount();
            return;
        }

        galleryGrid.style.height = listTotalHeight + 'px';
        galleryGrid.style.minHeight = '';

        const viewH = containerHeight || galleryScroll.clientHeight;
        const rowStep = rowH + gap;
        const totalItems = displayImages.length;

        const firstVisibleRow = Math.max(0, Math.floor(scrollTop / rowStep) - OVERSCAN);
        const lastVisibleRow = Math.min(Math.ceil(totalItems / 2) - 1, Math.ceil((scrollTop + viewH) / rowStep) + OVERSCAN);
        const firstVisibleIdx = firstVisibleRow * 2;
        const lastVisibleIdx = Math.min(totalItems - 1, (lastVisibleRow + 1) * 2 - 1);

        const fragment = document.createDocumentFragment();
        for (let i = firstVisibleIdx; i <= lastVisibleIdx; i++) {
            const row = Math.floor(i / 2);
            const col = i % 2;
            const x = col * (colW + gap);
            const y = row * rowStep;
            const layoutInfo = { layout: 'list', x: x, y: y, w: colW, h: rowH };
            fragment.appendChild(createImageCard(displayImages[i], layoutInfo));
        }

        galleryGrid.appendChild(fragment);
        updateImageCount();
        _prewarmThumbnails(displayImages, 0, lastVisibleIdx + 1);
    }

    /**
     * 全量渲染网格（不使用虚拟滚动）
     * 用于搜索结果等需要一次性展示所有图片的场景
     * @param {Array} displayImages - 要渲染的图片数组
     */
    function renderGridFull(displayImages) {
        galleryGrid.className = 'gallery-grid';
        updateGridColumns();

        const totalItems = displayImages.length;
        const fragment = document.createDocumentFragment();
        for (let i = 0; i < totalItems; i++) {
            fragment.appendChild(createImageCard(displayImages[i]));
        }
        galleryGrid.innerHTML = '';
        galleryGrid.appendChild(fragment);
        // 重置渲染范围，确保后续 renderGrid 能正确全量重建
        renderedRange = { start: -1, end: -1 };
        _lastRenderScrollTop = scrollTop;
        updateImageCount();
    }

    function updateVirtualScroll() {
        const displayImages = isFilteringActive ? filteredImages : images;
        if (displayImages.length === 0) return;
        if (currentLayout === 'grid') {
            renderGrid(displayImages);
        } else if (currentLayout === 'masonry') {
            renderMasonry(displayImages);
        } else if (currentLayout === 'pinterest') {
            renderPinterest(displayImages);
        } else if (currentLayout === 'list') {
            renderList(displayImages);
        }
    }

    function updateGridColumns() {
        galleryGrid.style.gridTemplateColumns = `repeat(auto-fill, minmax(${thumbnailSize}px, 1fr))`;
    }

    function getColumnCount() {
        const containerWidth = galleryScroll.clientWidth - 24;
        const gap = 8;
        return Math.max(1, Math.floor((containerWidth + gap) / (thumbnailSize + gap)));
    }

    // ==================== 图片卡片 ====================

    // 收藏状态缓存，避免每次渲染都查询 IndexedDB
    let favoriteCache = new Map();
    let favoriteCacheDirty = true;

    async function refreshFavoriteCache() {
        try {
            const favPaths = await Storage.getAllFavorites();
            favoriteCache = new Map(favPaths.map(p => [p, true]));
            favoriteCacheDirty = false;
        } catch (e) {
            // 缓存刷新失败，使用现有缓存
        }
    }

    function isFavCached(path) {
        return favoriteCache.has(path);
    }

    function formatFileSize(bytes) {
        if (!bytes || bytes <= 0) return '';
        if (bytes < 1024) return bytes + ' B';
        if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + ' KB';
        if (bytes < 1024 * 1024 * 1024) return (bytes / (1024 * 1024)).toFixed(1) + ' MB';
        return (bytes / (1024 * 1024 * 1024)).toFixed(2) + ' GB';
    }

    function createImageCard(imgData, masonryLayout) {
        const card = document.createElement('div');
        card.className = 'image-card';
        card.dataset.path = imgData.path;

        if (selectedImages.has(imgData.path)) {
            card.classList.add('selected');
        }
        if (imgData.path === activeImagePath) {
            card.classList.add('active');
        }

        // ★ 收藏边框立即显示，不延迟
        if (isFavCached(imgData.path)) {
            card.classList.add('favorite');
        }

        const wrapper = document.createElement('div');
        wrapper.className = 'card-img-wrapper';

        const img = document.createElement('img');
        img.loading = 'lazy';

        if (imgData.thumbnailUrl) {
            img.className = 'loading';
            img.alt = imgData.name;
            img.decoding = 'async';
            img.addEventListener('error', function retryLoad() {
                let retries = parseInt(this.dataset.retryCount) || 0;
                if (retries < 3) {
                    retries++;
                    this.dataset.retryCount = retries;
                    const delay = 500 * retries + Math.random() * 500;
                    const baseUrl = imgData.thumbnailUrl || this.dataset.src || this.src;
                    if (baseUrl) {
                        setTimeout(() => {
                            this.src = '';
                            this.src = baseUrl.replace(/[&?]_r=[^&]*/g, '') + (baseUrl.includes('?') ? '&' : '?') + '_r=' + Math.random().toString(36).slice(2);
                        }, delay);
                    }
                } else {
                    this.style.display = 'none';
                    const errorDiv = document.createElement('div');
                    errorDiv.className = 'img-error';
                    errorDiv.textContent = 'Load failed';
                    if (this.parentNode) this.parentNode.insertBefore(errorDiv, this);
                }
            });
            // ★ 直接赋 src：卡片插入时立刻开始下载，不等 Observer 异步回调
            //   对于虚拟滚动，只有视窗附近的卡片才会被创建，因此直接加载是正确的
            img.src = imgData.thumbnailUrl;
        } else {
            img.className = 'loading placeholder-loading';
            img.alt = imgData.name;
            img.style.opacity = '0.3';
        }

        if (currentLayout === 'grid') {
            // ★ wrapper 固定宽高，不随图片原始尺寸变化
            wrapper.style.width = '100%';
            wrapper.style.height = thumbnailSize + 'px';
            wrapper.style.overflow = 'hidden';
            wrapper.style.background = '#000';
            wrapper.style.flexShrink = '0';
            // ★ img 充满 wrapper，object-fit:contain 保持比例不裁剪
            img.style.width = '100%';
            img.style.height = '100%';
            img.style.objectFit = 'contain';
            img.style.display = 'block';
            img.style.flexShrink = '0';
            // ★ 淡入（修复竞态：图片若已从缓存瞬间加载，load 事件不会再触发）
            img.style.opacity = '0';
            img.style.transition = 'opacity 0.2s ease';
            const handleLoad = () => { img.style.opacity = '1'; };
            if (img.complete && img.naturalWidth > 0) {
                handleLoad();
            } else {
                img.addEventListener('load', handleLoad, { once: true });
            }
        } else if (masonryLayout && masonryLayout.layout === 'list') {
            // 列表模式：两列，缩略图在左（保持原始比例，不裁剪），详细信息在右
            card.style.left = masonryLayout.x + 'px';
            card.style.top = masonryLayout.y + 'px';
            card.style.width = masonryLayout.w + 'px';
            card.style.height = masonryLayout.h + 'px';

            // 计算缩略图宽高（保留原始比例，高度固定为 thumbnailSize）
            const imgW = imgData.width || (imgData.height ? Math.round(imgData.height * 4/3) : thumbnailSize);
            const imgH = imgData.height || (imgData.width ? Math.round(imgData.width * 3/4) : thumbnailSize);
            const ratio = (imgW && imgH && imgH > 0) ? imgW / imgH : 4/3;
            const thumbH = thumbnailSize;
            const thumbW = Math.round(thumbH * ratio);

            wrapper.style.width = thumbW + 'px';
            wrapper.style.height = thumbH + 'px';
            wrapper.style.minWidth = thumbW + 'px';
            wrapper.style.maxHeight = thumbH + 'px';
            wrapper.style.overflow = 'hidden';
            wrapper.style.background = '#000';
            wrapper.style.display = 'block';
            wrapper.style.position = 'relative';
            wrapper.style.flexShrink = '0';

            img.style.position = 'absolute';
            img.style.inset = '0';
            img.style.width = '100%';
            img.style.height = '100%';
            img.style.objectFit = 'contain';

            img.style.opacity = '0';
            img.style.transition = 'opacity 0.25s ease';
            const handleListLoad = () => { img.style.opacity = '1'; };
            if (img.complete && img.naturalWidth > 0) {
                handleListLoad();
            } else {
                img.addEventListener('load', handleListLoad, { once: true });
            }
        } else if (masonryLayout && masonryLayout.layout === 'pinterest') {
            const item = masonryLayout.item;
            card.style.left = item.x + 'px';
            card.style.top = item.y + 'px';
            card.style.width = item.w + 'px';
            card.style.height = item.h + 'px';

            wrapper.style.position = 'absolute';
            wrapper.style.inset = '0';
            wrapper.style.overflow = 'hidden';
            wrapper.style.background = '#000';
            wrapper.style.display = 'block';

            img.style.position = 'absolute';
            img.style.inset = '0';
            img.style.width = '100%';
            img.style.height = '100%';
            img.style.objectFit = 'cover';

            // ★ 淡入
            img.style.opacity = '0';
            img.style.transition = 'opacity 0.25s ease';
            const handlePinterestLoad = () => { img.style.opacity = '1'; };
            if (img.complete && img.naturalWidth > 0) {
                handlePinterestLoad();
            } else {
                img.addEventListener('load', handlePinterestLoad, { once: true });
            }
        } else if (masonryLayout) {
            card.style.left = masonryLayout.x + 'px';
            card.style.top = masonryLayout.y + 'px';
            card.style.width = masonryLayout.w + 'px';
            card.style.height = masonryLayout.h + 'px';

            wrapper.style.position = 'absolute';
            wrapper.style.inset = '0';
            wrapper.style.overflow = 'hidden';
            wrapper.style.background = '#000';
            wrapper.style.display = 'block';

            img.style.position = 'absolute';
            img.style.inset = '0';
            img.style.width = '100%';
            img.style.height = '100%';
            img.style.objectFit = 'cover';

            // ★ 淡入（修复竞态：图片若已从缓存瞬间加载，load 事件不会再触发）
            img.style.opacity = '0';
            img.style.transition = 'opacity 0.25s ease';
            const handleMasonryLoad = () => { img.style.opacity = '1'; };
            if (img.complete && img.naturalWidth > 0) {
                handleMasonryLoad();
            } else {
                img.addEventListener('load', handleMasonryLoad, { once: true });
            }
        } else {
            wrapper.style.display = 'block';
            wrapper.style.background = '#000';
            wrapper.style.minHeight = '180px';
            img.style.width = '100%';
            img.style.display = 'block';
        }

        wrapper.appendChild(img);

        // 视频文件叠加播放图标
        if (imgData.isVideo) {
            const videoIcon = document.createElement('div');
            videoIcon.className = 'card-video-icon';
            videoIcon.innerHTML = '<svg viewBox="0 0 24 24" width="48" height="48"><polygon points="5,3 19,12 5,21" fill="white" opacity="0.9"/></svg>';
            wrapper.appendChild(videoIcon);
        }

        card.appendChild(wrapper);

        // ★ card-info 骨架立即创建，占住最终高度，避免后续文字填充时卡片跳动
        const info = document.createElement('div');
        info.className = 'card-info';

        const filename = document.createElement('span');
        filename.className = 'card-filename';
        filename.title = imgData.name;
        // 用零宽空格占位，撑开行高但不显示实际文字
        filename.textContent = '​';

        const resolution = document.createElement('span');
        resolution.className = 'card-resolution';
        resolution.textContent = '​';

        const promptBadge = document.createElement('span');
        promptBadge.className = 'card-prompt-count-badge';
        promptBadge.style.display = 'none';

        info.appendChild(filename);
        info.appendChild(resolution);
        info.appendChild(promptBadge);
        card.appendChild(info);

        // ★ 文件名立即填充（数据已在内存，无需延迟）
        filename.textContent = imgData.name;
        filename.title = imgData.name;

        // ★ 列表模式：额外填充详细信息字段
        if (masonryLayout && masonryLayout.layout === 'list') {
            const format = (imgData.name || '').split('.').pop().toUpperCase();
            const dimensions = (imgData.width && imgData.height)
                ? `${imgData.width} × ${imgData.height}`
                : '';
            const sizeStr = formatFileSize(imgData.size || 0);
            const modTime = imgData.lastModified
                ? new Date(imgData.lastModified).toLocaleDateString()
                : '';
            const folderPath = imgData.folder || '';
            const rootPath = imgData.rootPath || '';

            // 用 card-info 填充详细信息
            const meta = document.createElement('div');
            meta.className = 'card-meta';

            const addMeta = (text) => {
                const span = document.createElement('span');
                span.className = 'card-meta-item';
                span.textContent = text;
                meta.appendChild(span);
            };

            if (dimensions) addMeta(dimensions);
            if (sizeStr) addMeta(sizeStr);
            if (format) addMeta(format);
            if (modTime) addMeta(modTime);

            info.appendChild(meta);

            // 文件夹路径（如果有）
            if (folderPath || rootPath) {
                const folder = document.createElement('div');
                folder.className = 'card-folder';
                folder.title = rootPath ? `${rootPath}/${folderPath}` : folderPath;
                folder.textContent = rootPath ? `${rootPath} / ${folderPath}` : folderPath;
                info.appendChild(folder);
            }

            // 隐藏 resolution 和 promptBadge（list 模式不需要）
            resolution.style.display = 'none';
        }

        // 把已创建的元素引用存到 card 上，供 _scheduleCardDetails 填充后续信息
        card._infoFilename = filename;
        card._infoResolution = resolution;
        card._infoPromptBadge = promptBadge;

        /**
         * 点击时按需解析 URL
         */
        async function resolveAndOpen(data) {
            if (!data.url && data.file) {
                try {
                    const objectUrl = URL.createObjectURL(data.file);
                    data.url = objectUrl;
                    data.thumbnailUrl = objectUrl;
                    data._loaded = true;
                } catch (err) {
                    console.warn('[Gallery] 创建 ObjectURL 失败:', data.name, err);
                }
            }
            if (typeof ImageViewer !== 'undefined' && ImageViewer.open) {
                ImageViewer.open(data);
            }
        }

        card.addEventListener('click', (e) => {
            if (isEditMode) {
                if (e.shiftKey) {
                    rangeSelect(imgData.path);
                } else if (e.ctrlKey || e.metaKey) {
                    toggleSelection(imgData.path);
                } else {
                    toggleSelection(imgData.path);
                }
            } else if (typeof DetailPanel !== 'undefined' && DetailPanel.getLocked && DetailPanel.getLocked()) {
                if (!e.ctrlKey && !e.metaKey && !e.shiftKey) {
                    resolveAndOpen(imgData);
                }
            } else {
                if (e.ctrlKey || e.metaKey) {
                    toggleSelection(imgData.path);
                } else if (e.shiftKey) {
                    rangeSelect(imgData.path);
                } else {
                    const currentDetail = (typeof DetailPanel !== 'undefined' && DetailPanel.getCurrentImage)
                        ? DetailPanel.getCurrentImage() : null;
                    if (currentDetail && currentDetail.path === imgData.path) {
                        resolveAndOpen(imgData);
                    } else {
                        if (onImageClick) {
                            setActiveImage(imgData.path);
                            onImageClick(imgData);
                        }
                    }
                }
            }
        });

        card.addEventListener('dblclick', (e) => {
            if (typeof DetailPanel !== 'undefined' && DetailPanel.getLocked && DetailPanel.getLocked()) {
                return;
            }
            resolveAndOpen(imgData);
        });

        card.addEventListener('contextmenu', (e) => {
            e.preventDefault();
            if (typeof ImageContextMenu !== 'undefined') {
                ImageContextMenu.show(e.clientX, e.clientY, imgData.path, imgData.rootPath, imgData.folder);
            }
        });

        // --- 拖拽：DownloadURL 格式（指定文件名+原图URL）+ 默认位图 fallback ---
        img.addEventListener('dragstart', (e) => {
            const ext = imgData.name.split('.').pop().toLowerCase();
            const mimeMap = { jpg: 'image/jpeg', jpeg: 'image/jpeg', png: 'image/png', gif: 'image/gif', webp: 'image/webp', bmp: 'image/bmp', svg: 'image/svg+xml' };
            const mime = mimeMap[ext] || 'image/png';
            e.dataTransfer.setData('DownloadURL', mime + ':' + imgData.name + ':' + imgData.url);
            e.dataTransfer.setData('text/uri-list', imgData.url);
            e.dataTransfer.setData('text/plain', imgData.path);
            e.dataTransfer.effectAllowed = 'copy';
        });

        // ★ 延迟创建非核心 UI（overlay、操作按钮、文字信息）
        _scheduleCardDetails(card, wrapper, imgData);

        return card;
    }

    // ★ phase2
    // overlay / hoverActions / card-info（requestIdleCallback 延迟创建）
    function _scheduleCardDetails(card, wrapper, imgData) {
        const schedule = window.requestIdleCallback || function (cb) { return setTimeout(cb, 0); };
        const cancel = window.cancelIdleCallback || clearTimeout;
        let handle;

        function build() {
            // 卡片已从 DOM 移除（虚拟滚动回收），放弃构建
            if (!card.isConnected) return;

            // --- card-info 文字填充（文件名已在 createImageCard 立即填充，此处补充分辨率等）---
            if (card._infoResolution && imgData.metadata && imgData.metadata.params && imgData.metadata.params['Size']) {
                card._infoResolution.textContent = imgData.metadata.params['Size'];
            }
            if (card._infoPromptBadge && showPromptCount) {
                getPromptCount(imgData.path).then(count => {
                    if (count >= 2 && card._infoPromptBadge && card.isConnected) {
                        card._infoPromptBadge.textContent = count;
                        card._infoPromptBadge.style.display = '';
                    }
                });
            }

            // --- overlay（hover 覆盖层）---
            const overlay = document.createElement('div');
            overlay.className = 'card-overlay';

            const favIcon = document.createElement('span');
            favIcon.className = 'card-favorite-icon';
            favIcon.innerHTML = '<span class="icon icon-favorite-on"></span>';
            favIcon.style.display = isFavCached(imgData.path) ? 'block' : 'none';

            overlay.appendChild(favIcon);
            wrapper.appendChild(overlay);

            // --- hoverActions（悬停操作按钮）---
            const hoverActions = document.createElement('div');
            hoverActions.className = 'card-hover-actions';

            const favBtn = document.createElement('button');
            favBtn.className = 'card-action-btn fav-btn';
            favBtn.title = t('gallery.favorite_title');
            if (isFavCached(imgData.path)) {
                favBtn.innerHTML = '<span class="icon icon-favorite-on"></span>';
                favBtn.classList.add('active');
                favBtn.dataset.favorited = 'true';
            } else {
                favBtn.innerHTML = '<span class="icon icon-favorite-off"></span>';
                favBtn.dataset.favorited = 'false';
            }

            favBtn.addEventListener('click', async (e) => {
                e.stopPropagation();
                const nowFav = await Storage.toggleFavorite(imgData.path);
                if (nowFav) {
                    favBtn.innerHTML = '<span class="icon icon-favorite-on"></span>';
                    favBtn.classList.add('active');
                    favBtn.dataset.favorited = 'true';
                    card.classList.add('favorite');
                    favoriteCache.set(imgData.path, true);
                    showToast(t('detail.favorited'), 'success');
                } else {
                    favBtn.innerHTML = '<span class="icon icon-favorite-off"></span>';
                    favBtn.classList.remove('active');
                    favBtn.dataset.favorited = 'false';
                    card.classList.remove('favorite');
                    favoriteCache.delete(imgData.path);
                    showToast(t('detail.unfavorited'), 'info');
                }
                // 如果正在按收藏筛选，取消收藏时刷新视图让卡片立即消失
                if (currentFavoriteFilter && !nowFav) refreshCurrentFilteredView();
            });

            const tagBtn = document.createElement('button');
            tagBtn.className = 'card-action-btn tag-btn';
            tagBtn.title = t('gallery.select_tag_title');
            tagBtn.innerHTML = '<span class="icon icon-tag"></span>';

            const tagDropdown = document.createElement('div');
            tagDropdown.className = 'card-tag-dropdown';
            tagDropdown._triggerBtn = tagBtn;
            document.body.appendChild(tagDropdown);

            tagBtn.addEventListener('click', async (e) => {
                e.stopPropagation();
                document.querySelectorAll('.card-tag-dropdown.show').forEach(d => {
                    if (d !== tagDropdown) d.classList.remove('show');
                });
                if (tagDropdown.classList.contains('show')) {
                    tagDropdown.classList.remove('show');
                    return;
                }
                tagDropdown.innerHTML = '<div class="card-tag-dropdown-empty">' + t('gallery.loading') + '</div>';
                tagDropdown.classList.add('show');
                positionTagDropdown(tagDropdown, tagBtn);
                await populateTagDropdown(tagDropdown, imgData.path);
                positionTagDropdown(tagDropdown, tagBtn);
            });

            hoverActions.appendChild(favBtn);
            hoverActions.appendChild(tagBtn);
            wrapper.appendChild(hoverActions);
        }

        handle = schedule(build);
        // 保存 handle 以便必要时取消（卡片被移除时不做无效构建）
        card._detailsHandle = handle;
        card._detailsCancel = cancel;
    }

    // ===== 标签下拉菜单自适应定位 =====
    function positionTagDropdown(dropdown, button) {
        const btnRect = button.getBoundingClientRect();
        const vw = window.innerWidth;
        const vh = window.innerHeight;

        const itemCount = dropdown.querySelectorAll('.card-tag-dropdown-item').length;
        // 动态列数：≤10 个标签单列，>10 个双列（忽略手动列模式切换）
        const cols = itemCount > 10 ? 2 : 1;
        const ddWidth = cols === 2 ? 380 : 220;

        dropdown.style.columnCount = '';
        dropdown.style.gridTemplateColumns = cols === 2 ? '1fr 1fr' : '1fr';
        dropdown.style.width = ddWidth + 'px';
        dropdown.style.left = '';
        dropdown.style.right = '';
        dropdown.style.top = '';
        dropdown.style.bottom = '';
        dropdown.style.maxHeight = '';

        // 当标签少于 targetVisible 时，不设 maxHeight，让内容自然展示
        // 当标签超出 targetVisible 时，限制高度并滚动
        const targetVisible = 50;
        if (itemCount > targetVisible) {
            const rowH = 44;
            const targetRows = Math.ceil(targetVisible / cols);
            const maxH = Math.min(targetRows * rowH + 20, vh * 0.82);
            dropdown.style.maxHeight = maxH + 'px';
        } else {
            dropdown.style.maxHeight = 'none';
        }

        // 水平方向：优先右展，不够则左展
        const spaceRight = vw - btnRect.left;
        const spaceLeft = btnRect.right;

        if (spaceRight >= ddWidth + 8) {
            dropdown.style.left = btnRect.left + 'px';
        } else if (spaceLeft >= ddWidth + 8) {
            dropdown.style.left = (btnRect.right - ddWidth) + 'px';
        } else if (spaceRight >= spaceLeft) {
            dropdown.style.left = btnRect.left + 'px';
            dropdown.style.width = Math.min(ddWidth, spaceRight - 8) + 'px';
            dropdown.style.gridTemplateColumns = '1fr';
        } else {
            dropdown.style.left = (btnRect.right - Math.min(ddWidth, spaceLeft - 8)) + 'px';
            dropdown.style.width = Math.min(ddWidth, spaceLeft - 8) + 'px';
            dropdown.style.gridTemplateColumns = '1fr';
        }

        // 垂直方向：优先下展，不够则上展
        const spaceBelow = vh - btnRect.bottom;
        const spaceAbove = btnRect.top;
        if (dropdown.style.maxHeight === 'none') {
            if (spaceBelow >= spaceAbove) {
                dropdown.style.top = (btnRect.bottom + 4) + 'px';
            } else {
                dropdown.style.bottom = (vh - btnRect.top + 4) + 'px';
            }
        } else {
            const currentMaxH = parseFloat(dropdown.style.maxHeight);
            if (spaceBelow >= currentMaxH + 8 || spaceBelow >= spaceAbove) {
                dropdown.style.top = (btnRect.bottom + 4) + 'px';
                dropdown.style.maxHeight = Math.min(currentMaxH, spaceBelow - 8) + 'px';
            } else {
                dropdown.style.bottom = (vh - btnRect.top + 4) + 'px';
                dropdown.style.maxHeight = Math.min(currentMaxH, spaceAbove - 8) + 'px';
            }
        }

        // 裁剪到视口内
        const leftVal = parseFloat(dropdown.style.left) || 0;
        if (leftVal < 4) dropdown.style.left = '4px';
        const actualWidth = parseFloat(dropdown.style.width);
        if (leftVal + actualWidth > vw - 4) {
            dropdown.style.left = Math.max(4, vw - actualWidth - 4) + 'px';
        }
    }

    // ===== 填充标签下拉菜单 =====
    async function populateTagDropdown(dropdown, imagePath) {
        try {
            const allTags = await Storage.getAllTags();
            const imageTags = await Storage.getTagsForImage(imagePath);
            const imageTagIds = new Set(imageTags.map(t => t.id));

            if (!allTags || allTags.length === 0) {
                dropdown.innerHTML = '<div class="card-tag-dropdown-empty">' + t('detail.no_tags_create_first') + '</div>';
                return;
            }

            dropdown.innerHTML = '';

            for (const tag of allTags) {
                const item = document.createElement('div');
                item.className = 'card-tag-dropdown-item';
                if (imageTagIds.has(tag.id)) {
                    item.classList.add('checked');
                }

                if (tag.tagType === 'html') {
                    // HTML tag: 按比例缩放到下拉菜单宽度
                    const origW = tag.htmlWidth || 120;
                    const origH = tag.htmlHeight || 40;
                    const maxW = 160;
                    const scale = Math.min(1, maxW / origW);
                    const w = Math.round(origW * scale);
                    const h = Math.round(origH * scale);
                    item.style.cssText = 'min-height:' + (h + 8) + 'px;display:flex;align-items:center;gap:6px;padding:4px 8px;';
                    item.title = tag.name;
                    const htmlBox = document.createElement('span');
                    htmlBox.style.cssText = 'display:inline-flex;align-items:center;justify-content:center;width:' + w + 'px;height:' + h + 'px;overflow:hidden;vertical-align:middle;border:1px solid var(--border-color);border-radius:2px;flex-shrink:0;';
                    const inner = document.createElement('div');
                    inner.style.cssText = 'display:inline-block;transform-origin:center center;';
                    htmlBox.appendChild(inner);
                    const scopeId = 'dd-tag-scope-' + tag.id;
                    htmlBox.setAttribute('data-tag-scope', scopeId);
                    let processedCode = (typeof WailsBridge !== 'undefined' && WailsBridge.fixRelativeUrls)
                        ? WailsBridge.fixRelativeUrls(tag.htmlCode || '') : (tag.htmlCode || '');
                    let scopedHtml = processedCode.replace(/<style([^>]*)>/g, (_, attrs) => {
                        return '<style' + attrs + ' data-scope="' + scopeId + '">';
                    });
                    inner.innerHTML = scopedHtml;
                    inner.querySelectorAll('style[data-scope]').forEach(styleEl => {
                        const raw = styleEl.textContent;
                        if (!raw) return;
                        const scoped = raw.replace(/([^{}]*\{)/g, (rule) => {
                            const trimmed = rule.trim();
                            if (/^@|^\d+(\.\d+)?%|^(from|to)\b/i.test(trimmed)) return rule;
                            if (/\b(html|body|:root)\b|\*/.test(trimmed)) return '';
                            return rule.replace(/(^|,)\s*/g, (sep) => {
                                return sep + '[data-tag-scope="' + scopeId + '"] ';
                            });
                        });
                        styleEl.textContent = (typeof WailsBridge !== 'undefined' && WailsBridge.fixRelativeUrls) ? WailsBridge.fixRelativeUrls(scoped) : scoped;
                        styleEl.removeAttribute('data-scope');
                    });
                    inner.querySelectorAll('script').forEach(s => {
                        try { eval(s.textContent); } catch(e) {}
                    });
                    // 等比缩放
                    requestAnimationFrame(() => {
                        const rect = inner.getBoundingClientRect();
                        const nw = rect.width || w;
                        const nh = rect.height || h;
                        const scale = Math.min(w / nw, h / nh, 1);
                        inner.style.transform = `scale(${scale})`;
                    });
                    item.appendChild(htmlBox);
                } else if (tag.tagType === 'avatar') {
                    // Avatar tag: match sidebar style (compact version)
                    const shape = tag.shape || 'round';
                    const rad = shape === 'sharp' ? 2 : shape === 'soft' ? 6 : 14;
                    const thumbRad = shape === 'sharp' ? 2 : shape === 'soft' ? 4 : 10;
                    const itemBg = tag.bgColor || 'var(--bg-secondary)';

                    // Outer wrapper matching sidebar tag-item-avatar
                    const avWrap = document.createElement('span');
                    avWrap.style.cssText = `display:inline-flex;align-items:center;gap:0;background:${itemBg};border:0.5px solid var(--border-color);border-radius:${rad}px;overflow:hidden;padding:0;flex-shrink:0;`;

                    if (tag.avatarData) {
                        const avImg = document.createElement('img');
                        avImg.src = (typeof WailsBridge !== 'undefined' && WailsBridge.getAvatarUrl)
                            ? WailsBridge.getAvatarUrl(tag.avatarData) : tag.avatarData;
                        avImg.style.cssText = `width:24px;height:24px;border-radius:${thumbRad}px 0 0 ${thumbRad}px;object-fit:cover;flex-shrink:0;`;
                        avWrap.appendChild(avImg);
                    } else {
                        const avInit = document.createElement('span');
                        avInit.style.cssText = `width:24px;height:24px;border-radius:${thumbRad}px 0 0 ${thumbRad}px;display:flex;align-items:center;justify-content:center;font-size:11px;font-weight:500;flex-shrink:0;background:${itemBg};color:${tag.color || '#fff'};`;
                        avInit.textContent = (tag.name || '?')[0].toUpperCase();
                        avWrap.appendChild(avInit);
                    }

                    if (tag.showName !== false) {
                        const avName = document.createElement('span');
                        avName.textContent = tag.name;
                        avName.style.cssText = `font-size:12px;font-weight:500;padding:0 10px;white-space:nowrap;color:${tag.color || '#ffffff'};background:${itemBg};border-radius:0 ${rad}px ${rad}px 0;`;
                        avWrap.appendChild(avName);
                    }
                    item.appendChild(avWrap);
                } else {
                    // Mini tag badge preview
                    const miniTag = document.createElement('span');
                    miniTag.className = 'tag-badge tag-size-sm';
                    if (typeof TagStyle !== 'undefined') {
                        TagStyle.apply(miniTag, tag);
                    } else {
                        miniTag.style.background = tag.color || '#9b59b6';
                        miniTag.style.color = '#fff';
                    }
                    var isIconOnly = tag.iconOnly || (!tag.name && tag.icon);
                    var iconSize = isIconOnly && tag.iconOnlySize ? tag.iconOnlySize : null;
                    if (tag.icon) {
                        if (tag.icon.indexOf('data:') === 0) {
                            var iconImg = document.createElement('img');
                            iconImg.className = 'tag-icon-img';
                            iconImg.src = tag.icon;
                            iconImg.alt = '';
                            if (iconSize) { iconImg.style.height = iconSize + 'px'; }
                            miniTag.appendChild(iconImg);
                        } else {
                            miniTag.textContent = tag.icon + ' ';
                        }
                    }
                    if (tag.showName !== false) {
                        const tagName = document.createElement('span');
                        tagName.textContent = tag.name;
                        miniTag.appendChild(tagName);
                    }
                    item.appendChild(miniTag);
                }

                item.addEventListener('click', async (e) => {
                    e.stopPropagation();
                    if (imageTagIds.has(tag.id)) {
                        await Storage.removeTagFromImage(imagePath, tag.id);
                        item.classList.remove('checked');
                        imageTagIds.delete(tag.id);
                        showToast(t('toast.tag_removed_with_name').replace('{name}', tag.name), 'info');
                        // 如果正在按标签筛选且移除的是当前筛选标签，刷新视图
                        if (currentTagFilter === tag.id) refreshCurrentFilteredView();
                    } else {
                        await Storage.addTagToImage(imagePath, tag.id);
                        item.classList.add('checked');
                        imageTagIds.add(tag.id);
                        showToast(t('toast.tag_added_with_name').replace('{name}', tag.name), 'success');
                    }
                });

                dropdown.appendChild(item);
            }
        } catch (err) {
            console.error('[Gallery] 加载标签失败:', err);
            dropdown.innerHTML = '<div class="card-tag-dropdown-empty">' + t('detail.folder_load_failed') + '</div>';
        }
    }

    // ==================== 选择管理 ====================

    function toggleSelection(path) {
        if (selectedImages.has(path)) {
            selectedImages.delete(path);
        } else {
            selectedImages.add(path);
        }
        updateSelectionUI();
    }

    function rangeSelect(path) {
        const displayImages = isFilteringActive ? filteredImages : images;
        const clickedIndex = displayImages.findIndex(img => img.path === path);
        if (clickedIndex === -1) return;

        let minSelected = clickedIndex;
        let maxSelected = clickedIndex;
        for (const selPath of selectedImages) {
            const idx = displayImages.findIndex(img => img.path === selPath);
            if (idx !== -1) {
                minSelected = Math.min(minSelected, idx);
                maxSelected = Math.max(maxSelected, idx);
            }
        }

        for (let i = minSelected; i <= maxSelected; i++) {
            selectedImages.add(displayImages[i].path);
        }
        updateSelectionUI();
    }

    function selectAll() {
        const displayImages = isFilteringActive ? filteredImages : images;
        for (const img of displayImages) {
            selectedImages.add(img.path);
        }
        updateSelectionUI();
    }

    function clearSelection() {
        selectedImages.clear();
        updateSelectionUI();
    }

    function setActiveImage(path) {
        if (activeImagePath === path) return;
        // 移除旧高亮
        if (activeImagePath) {
            const oldCard = galleryGrid.querySelector(`.image-card[data-path="${CSS.escape(activeImagePath)}"]`);
            if (oldCard) oldCard.classList.remove('active');
        }
        activeImagePath = path;
        // 添加新高亮
        const newCard = galleryGrid.querySelector(`.image-card[data-path="${CSS.escape(path)}"]`);
        if (newCard) newCard.classList.add('active');
    }

    function scrollToImage(path) {
        setActiveImage(path);
        const card = galleryGrid.querySelector(`.image-card[data-path="${CSS.escape(path)}"]`);
        if (card) {
            card.scrollIntoView({ block: 'center', behavior: 'instant' });
        }
    }

    function updateSelectionUI() {
        const cards = galleryGrid.querySelectorAll('.image-card');
        cards.forEach(card => {
            const path = card.dataset.path;
            card.classList.toggle('selected', selectedImages.has(path));
        });

        if (selectedImages.size > 0) {
            Sidebar.showBatchActions(selectedImages.size);
        } else {
            Sidebar.hideBatchActions();
        }

        if (onSelectionChange) {
            onSelectionChange(Array.from(selectedImages));
        }
    }

    function getSelectedImages() {
        const displayImages = isFilteringActive ? filteredImages : images;
        return displayImages.filter(img => selectedImages.has(img.path));
    }

    // ==================== 批量操作 ====================

    async function batchAddTag(tagId) {
        const selected = getSelectedImages();
        for (const img of selected) {
            await Storage.addTagToImage(img.path, tagId);
        }
        showToast(t('toast.batch_images_tagged').replace('{n}', selected.length), 'success');
    }

    async function batchToggleFavorite() {
        const selected = getSelectedImages();
        for (const img of selected) {
            await Storage.setFavorite(img.path, true);
        }
        // 标记缓存为脏，下次渲染时刷新
        favoriteCacheDirty = true;
        showToast(t('toast.batch_images_favorited').replace('{n}', selected.length), 'success');
        render();
    }

    async function batchRemoveFavorite() {
        const selected = getSelectedImages();
        if (selected.length === 0) {
            showToast(t('toast.no_selection'), 'warning');
            return;
        }
        for (const img of selected) {
            await Storage.setFavorite(img.path, false);
        }
        favoriteCacheDirty = true;
        clearSelection();
        await refreshCurrentFilteredView();
        showToast(t('toast.batch_images_unfavorited').replace('{n}', selected.length), 'success');
    }

    async function batchRemoveTag(tagId) {
        const selected = getSelectedImages();
        if (selected.length === 0) {
            showToast(t('toast.no_selection'), 'warning');
            return;
        }
        let removedCount = 0;
        for (const img of selected) {
            try {
                await Storage.removeTagFromImage(img.path, tagId);
                removedCount++;
            } catch (e) {
                // 该图片可能没有此标签，跳过
            }
        }
        clearSelection();
        await refreshCurrentFilteredView();
        showToast(t('toast.batch_tag_removed').replace('{n}', removedCount), 'success');
    }

    async function batchMove(targetFolderPath) {
        const selected = getSelectedImages();
        if (selected.length === 0) {
            showToast(t('toast.no_selection'), 'warning');
            return;
        }

        const paths = selected.map(img => img.path);
        const savedFilter = currentFolderFilter;

        let result;
        try {
            if (typeof WailsBridge !== 'undefined') {
                result = await WailsBridge.moveFiles(paths, targetFolderPath);
            } else {
                showToast(t('toast.move_not_supported'), 'error');
                return;
            }
        } catch (err) {
            console.error('[Gallery] batchMove error:', err);
            showToast(t('toast.move_failed') + ': ' + err.message, 'error');
            return;
        }

        if (!result || !result.success) {
            const errors = (result && result.errors) ? result.errors.join('; ') : '未知错误';
            showToast(t('toast.move_failed') + ': ' + errors, 'error');
            return;
        }

        let msg = `已移动 ${result.movedCount} 张图片`;
        if (result.errors && result.errors.length > 0) {
            msg += `，${result.errors.length} 个失败`;
        }
        showToast(msg, 'success');

        clearSelection();

        // 从后端重新加载，确保前后端状态一致
        if (typeof Sidebar !== 'undefined' && Sidebar.refreshFolderTree) {
            await Sidebar.refreshFolderTree();
        }
        if (savedFilter) {
            await filterByFolder(savedFilter, null, { forceRefresh: false });
        } else {
            await clearFilters();
        }
    }

    async function batchCopy(targetFolderPath) {
        const selected = getSelectedImages();
        if (selected.length === 0) {
            showToast(t('toast.no_selection'), 'warning');
            return;
        }

        const paths = selected.map(img => img.path);
        const savedFilter = currentFolderFilter;

        let result;
        try {
            if (typeof WailsBridge !== 'undefined') {
                result = await WailsBridge.copyFiles(paths, targetFolderPath);
            } else {
                showToast(t('toast.copy_not_supported'), 'error');
                return;
            }
        } catch (err) {
            console.error('[Gallery] batchCopy error:', err);
            showToast(t('toast.copy_failed') + ': ' + err.message, 'error');
            return;
        }

        if (!result || !result.success) {
            const errors = (result && result.errors) ? result.errors.join('; ') : '未知错误';
            showToast(t('toast.copy_failed') + ': ' + errors, 'error');
            return;
        }

        let msg = `已复制 ${result.movedCount} 张图片`;
        if (result.errors && result.errors.length > 0) {
            msg += `，${result.errors.length} 个失败`;
        }
        showToast(msg, 'success');

        clearSelection();

        if (typeof Sidebar !== 'undefined' && Sidebar.refreshFolderTree) {
            await Sidebar.refreshFolderTree();
        }
        if (savedFilter) {
            await filterByFolder(savedFilter, null, { forceRefresh: false });
        } else {
            await clearFilters();
        }
    }

    async function batchDelete() {
        const selected = getSelectedImages();
        if (!confirm(t('sidebar.confirm_delete_image').replace('{n}', selected.length))) return;

        // 收集要删除的 image ID，调用后端清理缩略图和数据库
        const ids = selected.map(img => img.id || img.ID).filter(Boolean);
        try {
            await WailsBridge.removeImages(ids);
        } catch (err) {
            console.error('[Gallery] 后端删除失败:', err);
        }

        for (const img of selected) {
            try {
                if (img.thumbnailUrl && img.thumbnailUrl.startsWith('blob:')) {
                    URL.revokeObjectURL(img.thumbnailUrl);
                }
                images = images.filter(i => i.path !== img.path);
                selectedImages.delete(img.path);
            } catch (err) {
                console.error('[Gallery] 删除失败:', img.path, err);
            }
        }

        importedRoots = importedRoots.filter(root => images.some(img => img.rootId === root.rootId));

        if (currentFolderFilter && !images.some(img => img.path === currentFolderFilter || img.path.startsWith(currentFolderFilter + '/'))) {
            currentFolderFilter = null;
        }

        clearSelection();
        if (currentTagFilter || currentFavoriteFilter) {
            await refreshCurrentFilteredView();
        } else {
            applyCurrentFilter();
            render();
        }
        showToast(t('toast.batch_images_deleted').replace('{n}', selected.length), 'success');
    }

    // ==================== 批量反推 ====================

    function updateBatchProgressUI() {
        const bar = document.getElementById('batchProgress');
        const text = document.getElementById('batchProgressText');
        const fill = document.getElementById('batchProgressFill');
        const btnPause = document.getElementById('btnBatchPause');
        if (bar) bar.style.display = 'flex';
        const progress = batchCompleted + batchFailed;
        if (text) text.textContent = batchFailed > 0 ? `${progress}/${batchTotal} (${t('toast.batch_failed_short').replace('{n}', batchFailed)})` : `${progress}/${batchTotal}`;
        if (fill) fill.style.width = batchTotal > 0 ? `${(progress / batchTotal) * 100}%` : '0%';
        if (btnPause) {
            btnPause.innerHTML = batchPaused
                ? '<span class="icon icon-play"></span>'
                : '<span class="icon icon-pause"></span>';
            btnPause.title = batchPaused ? '继续' : '暂停';
        }
    }

    function hideBatchProgressUI() {
        const bar = document.getElementById('batchProgress');
        if (bar) bar.style.display = 'none';
    }

    async function startBatchReverse() {
        const selected = getSelectedImages();
        if (selected.length === 0) {
            showToast(t('toast.select_image_first'), 'warning');
            return;
        }

        // 获取 API 配置
        let config = null;
        if (typeof Storage !== 'undefined') {
            const activeId = await Storage.getSetting('activeApiConfigId', null);
            if (activeId) {
                const configs = await Storage.getAllApiConfigs();
                config = configs.find(c => c.id === activeId) || null;
            }
            if (!config) config = await Storage.getDefaultApiConfig();
        }
        if (!config) {
            showToast(t('detail.api_not_configured'), 'warning');
            if (typeof Sidebar !== 'undefined') Sidebar.switchTab('api');
            return;
        }

        // 初始化
        batchQueue = selected.slice();
        batchTotal = batchQueue.length;
        batchCompleted = 0;
        batchFailed = 0;
        batchRunning = true;
        batchPaused = false;
        batchStopped = false;

        // 退出编辑模式，显示进度条
        if (typeof App !== 'undefined' && App.setEditMode) {
            App.setEditMode(false);
        }
        clearSelection();
        updateBatchProgressUI();

        // 绑定暂停/停止按钮
        const btnPause = document.getElementById('btnBatchPause');
        const btnStop = document.getElementById('btnBatchStop');
        const onPause = () => {
            batchPaused = !batchPaused;
            updateBatchProgressUI();
            if (!batchPaused && batchResolve) batchResolve();
        };
        const onStop = () => {
            batchStopped = true;
            batchPaused = false;
            if (batchResolve) batchResolve();
        };
        if (btnPause) btnPause.addEventListener('click', onPause);
        if (btnStop) btnStop.addEventListener('click', onStop);

        showToast(t('toast.batch_prompt_start').replace('{n}', batchTotal), 'info');

        // 逐个处理
        for (const img of batchQueue) {
            if (batchStopped) break;

            while (batchPaused && !batchStopped) {
                await new Promise(r => { batchResolve = r; });
            }
            if (batchStopped) break;

            try {
                await processBatchImage(img, config);
                batchCompleted++;
                if (typeof WailsBridge !== 'undefined' && WailsBridge.appendReverseLog) {
                    WailsBridge.appendReverseLog(img.name, img.path, '').catch(() => {});
                }
            } catch (err) {
                console.warn('[Gallery] 批量反推失败:', img.name, err.message);
                batchFailed++;
                const errMsg = err.message || String(err);
                if (typeof WailsBridge !== 'undefined' && WailsBridge.appendReverseLog) {
                    WailsBridge.appendReverseLog(img.name, img.path, errMsg).catch(() => {});
                }
            }
            updateBatchProgressUI();
        }

        // 完成
        batchRunning = false;
        const msg = batchStopped
            ? t('toast.batch_prompt_stopped').replace('{done}', batchCompleted).replace('{total}', batchTotal) + (batchFailed > 0 ? t('toast.batch_failed_suffix').replace('{n}', batchFailed) : '')
            : t('toast.batch_prompt_completed').replace('{done}', batchCompleted).replace('{total}', batchTotal) + (batchFailed > 0 ? t('toast.batch_failed_suffix').replace('{n}', batchFailed) : '');
        showToast(msg, batchStopped ? 'warning' : 'success');

        setTimeout(hideBatchProgressUI, 3000);

        // 解绑按钮
        if (btnPause) btnPause.removeEventListener('click', onPause);
        if (btnStop) btnStop.removeEventListener('click', onStop);
    }

    async function processBatchImage(img, config) {
        // 获取 base64
        let base64;
        if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
            try {
                const fileData = await WailsBridge.getImageFile(img.id);
                if (fileData && fileData.data) base64 = fileData.data;
            } catch (e) {}
        }
        if (!base64 && img.file) {
            base64 = await ApiService.fileToBase64(img.file);
        }
        if (!base64 && img.url) {
            base64 = await ApiService.urlToBase64(img.url);
        }
        if (!base64) throw new Error('无法获取图片数据');

        // 调用 AI API
        const result = await ApiService.reversePrompt(base64, config);

        // 保存提示词
        await Storage.addPromptVersion(img.path, {
            positivePrompt: result.positivePrompt || '',
            negativePrompt: result.negativePrompt || '',
            source: 'ai_generated'
        });
    }

    // ==================== 工具函数 ====================

    function isImageFileObject(file) {
        const ext = '.' + (file.name.split('.').pop() || '').toLowerCase();
        return IMAGE_EXTENSIONS.has(ext) || VIDEO_EXTENSIONS.has(ext);
    }

    function normalizeRelativePath(relativePath) {
        return String(relativePath || '').replace(/\\/g, '/').replace(/^\/+/, '');
    }

    function getRootNameFromFiles(files) {
        const sample = normalizeRelativePath(files[0]?.webkitRelativePath || files[0]?.name || '');
        const parts = sample.split('/').filter(Boolean);
        if (parts.length > 1) {
            return parts[0];
        }

        const folderHint = files[0]?.path || files[0]?.name || '';
        if (typeof folderHint === 'string' && folderHint.includes('/')) {
            return folderHint.split('/')[0] || '未命名文件夹';
        }

        return '导入文件夹';
    }

    function parseRelativePath(relativePath, rootName, hasStructuredRelativePaths = true) {
        const normalized = normalizeRelativePath(relativePath);

        if (!hasStructuredRelativePaths) {
            const fileName = normalized.split('/').filter(Boolean).pop() || '';
            return {
                relativePath: fileName,
                folder: '',
                fullPath: `${rootName}/${fileName}`
            };
        }

        const parts = normalized.split('/').filter(Boolean);
        const withoutRoot = parts[0] === rootName ? parts.slice(1) : parts;
        const fileName = withoutRoot[withoutRoot.length - 1] || parts[parts.length - 1] || '';
        const folderParts = withoutRoot.slice(0, -1);
        const folder = folderParts.join('/');
        const fullPath = folder ? `${rootName}/${folder}/${fileName}` : `${rootName}/${fileName}`;
        const relative = folder ? `${folder}/${fileName}` : fileName;

        return {
            relativePath: relative,
            folder,
            fullPath
        };
    }

    function createLocalImageId(relativePath, file) {
        const base = `${relativePath}::${file.size || 0}::${file.lastModified || 0}`;
        let hash = 0;
        for (let i = 0; i < base.length; i++) {
            hash = ((hash << 5) - hash) + base.charCodeAt(i);
            hash |= 0;
        }
        return 'local_' + Math.abs(hash);
    }

    function sortFolderNodes(nodes) {
        nodes.sort((a, b) => a.name.localeCompare(b.name, 'zh-CN'));
        for (const node of nodes) {
            if (node.children && node.children.length > 0) {
                sortFolderNodes(node.children);
            }
        }
    }

    function releaseImagesByRoot(rootPath) {
        const rootImages = images.filter(img => img.rootPath === rootPath || img.rootId === rootPath);
        for (const img of rootImages) {
            if (img._loaded && img.thumbnailUrl && img.thumbnailUrl.startsWith('blob:')) {
                URL.revokeObjectURL(img.thumbnailUrl);
            }
            img.url = null;
            img.thumbnailUrl = null;
            img._loaded = false;
            selectedImages.delete(img.path);
        }
    }

    function showLoading(show) {
        loadingIndicator.style.display = show ? 'flex' : 'none';
    }

    function updateImageCount() {
        const displayImages = isFilteringActive ? filteredImages : images;
        const label = I18n.t('toolbar.image_count');
        imageCountEl.textContent = `${displayImages.length} ${label}`;
    }

    // ==================== 按需解析元数据 ====================

    /**
     * 按需解析单张图片的元数据
     * 当用户点击图片时调用，避免批量导入时全部解析
     * @param {Object} imgData - 图片数据对象
     * @returns {Promise<Object|null>} 解析后的元数据，或 null
     */
    async function resolveMetadataOnDemand(imgData) {
        // 如果已有元数据，直接返回
        if (imgData.metadata) {
            console.log('[Gallery] resolveMetadataOnDemand: 已有元数据，直接返回', imgData.name);
            return imgData.metadata;
        }

        const ext = (imgData.name || '').split('.').pop().toLowerCase();
        if (!['png', 'jpg', 'jpeg', 'webp', 'mp4', 'webm', 'mkv'].includes(ext)) {
            console.log('[Gallery] resolveMetadataOnDemand: 不支持的文件格式', ext, imgData.name);
            return null;
        }

        console.log('[Gallery] resolveMetadataOnDemand: 开始解析', imgData.name, 'url:', imgData.url, '_fromServer:', imgData._fromServer, 'hasFile:', !!imgData.file);

        // 检查 MetadataParser 是否可用
        if (typeof MetadataParser === 'undefined') {
            console.warn('[Gallery] resolveMetadataOnDemand: MetadataParser 不可用');
            return null;
        }

        try {
            let meta;
            
            if (imgData.file) {
                // 浏览器环境：有 File 对象，直接解析
                console.log('[Gallery] resolveMetadataOnDemand: 使用 File 对象解析', imgData.name);
                meta = await MetadataParser.parseFile(imgData.file);
            } else if (imgData._fromServer) {
                // ★ Wails 环境：用 Go 端 ParseMetadata 本地读取（只读头部元数据，不下载全图）
                console.log('[Gallery] resolveMetadataOnDemand: 通过 Go ParseMetadata 本地解析, path:', imgData.path);
                if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
                    try {
                        meta = await WailsBridge.parseMetadata(imgData.path);
                        if (meta && (meta.prompt || Object.keys(meta.params || {}).length > 0 || Object.keys(meta.raw || {}).length > 0)) {
                            console.log('[Gallery] resolveMetadataOnDemand: Go 解析成功, prompt:', (meta.prompt || '').substring(0, 50));
                        } else {
                            meta = null;
                        }
                    } catch (e) {
                        console.warn('[Gallery] Go ParseMetadata 失败:', e.message);
                    }
                }
            } else {
                console.warn('[Gallery] resolveMetadataOnDemand: 无法获取图片数据', 'url:', imgData.url, '_fromServer:', imgData._fromServer, 'file:', !!imgData.file);
                return null;
            }

            imgData.metadata = meta;
            return meta;
        } catch (err) {
            console.warn('[Gallery] 按需解析元数据失败:', imgData.name, err);
            return null;
        }
    }

    // ==================== 重命名文件夹（虚拟重命名） ====================

    /**
     * 重命名已导入的根文件夹（仅修改显示名称，不影响实际文件路径）
     * @param {string} rootId - 根文件夹的唯一 ID
     * @param {string} newDisplayName - 新的显示名称
     * @returns {boolean} 是否成功
     */
    async function renameImportedRoot(rootId, newDisplayName) {
        // ★ 修复问题一：规范化 rootId 进行匹配，兼容斜杠风格不一致
        const normalizedRootId = (rootId || '').replace(/\\/g, '/');
        let root = importedRoots.find(r => {
            const rId = (r.rootId || '').replace(/\\/g, '/');
            const rPath = (r.path || '').replace(/\\/g, '/');
            return rId === normalizedRootId || rPath === normalizedRootId;
        });
        
        // ★ 修复问题一：如果 importedRoots 中找不到，自动创建条目
        // 这种情况发生在后端注册的文件夹（真实路径）尚未在 importedRoots 中注册时
        if (!root) {
            const folderName = rootId.split(/[\\/]/).pop() || rootId;
            root = {
                rootId: rootId,
                path: rootId,
                name: folderName,
                handleName: folderName,
                displayName: folderName
            };
            importedRoots.push(root);
            console.log('[Gallery] 自动创建 importedRoot 条目:', rootId);
        }
        
        const name = (newDisplayName || '').trim();
        if (!name) return false;
        
        // 更新 importedRoots 中的显示名称
        root.displayName = name;
        
        // 更新所有属于该根目录的图片的 displayName
        // ★ 修复问题一：同时匹配 rootId 和 rootPath，兼容不同来源的图片
        for (const img of images) {
            const imgRootId = (img.rootId || '').replace(/\\/g, '/');
            const imgRootPath = (img.rootPath || '').replace(/\\/g, '/');
            if (imgRootId === normalizedRootId || imgRootPath === normalizedRootId) {
                img.displayName = name;
            }
        }

        // ★ 修复问题二：await 持久化完成，避免 fire-and-forget 竞态导致刷新丢失
        try {
            await saveImportedRootsToServer();
            console.log('[Gallery] 重命名持久化成功:', rootId, '->', name);
        } catch (err) {
            console.error('[Gallery] 重命名持久化失败:', err.message);
            showToast(t('toast.rename_partial_save'), 'warning');
        }

        // ★ 通知外部刷新文件夹树（侧边栏）
        if (onFolderTreeChange) {
            onFolderTreeChange();
        }
        
        return true;
    }

    /**
     * 获取所有已导入的根目录信息
     * @returns {Array<{rootId: string, name: string, displayName: string, imageCount: number}>}
     */
    function getImportedRoots() {
        return importedRoots.map(r => ({
            rootId: r.rootId,
            name: r.name,
            displayName: r.displayName || r.name,
            imageCount: images.filter(img => img.rootId === r.rootId).length
        }));
    }

    /**
     * 手动添加一个 importedRoot 记录并持久化到后端
     * 用于通过输入路径导入文件夹时，添加导航栏记录
     * @param {Object} rootInfo - { rootId, path, name, handleName, displayName }
     */
    function addImportedRoot(rootInfo) {
        if (!rootInfo || !rootInfo.rootId) return;
        if (importedRoots.some(r => r.rootId === rootInfo.rootId)) return;

        importedRoots.push({
            rootId: rootInfo.rootId,
            path: rootInfo.path || rootInfo.rootId,
            name: rootInfo.name,
            handleName: rootInfo.handleName || rootInfo.name,
            displayName: rootInfo.displayName || rootInfo.name
        });
        // 注意：不在此处持久化，由调用者（如 openSystemFolderPicker）在 RPC 成功后负责
    }

    // ==================== 跨浏览器持久化：保存/恢复导入的文件夹信息 ====================

    /**
     * 将当前已导入的根目录信息保存到后端 settings.json
     * 这样更换浏览器后，导航栏仍能看到已导入的文件夹列表
     * 现在使用 markDirty 机制自动保存，不再需要手动 await
     */
    async function saveImportedRootsToServer() {
        try {
            const rootsData = importedRoots.map(r => ({
                path: r.rootId,
                name: r.name || '',
                displayName: r.displayName || r.name || '',
                folderType: r.folderType || '',
                handleName: r.handleName || '',
                addedAt: r.addedAt || new Date().toISOString()
            }));
            if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
                const result = await WailsBridge.saveRootsWithMeta(rootsData);
                if (result && result.success) {
                    console.log(`[Gallery] 已保存 ${rootsData.length} 个导入文件夹信息到 SQLite`);
                }
            }
        } catch (err) {
            console.warn('[Gallery] 保存导入文件夹信息失败:', err.message);
        }
    }

    /**
     * 从后端 settings.json 加载已保存的导入文件夹信息
     * 在 App 初始化时调用，用于恢复导航栏显示
     *
     * ★ 所有数据只从 user 文件夹中的 user-data.json 加载，
     *    不依赖任何浏览器缓存（IndexedDB/localStorage）
     *    
     * ★ 只恢复那些 images 数组中还有对应图片的文件夹，
     *    如果文件夹已被删除（images 中无对应图片），则不恢复。
     * @returns {Promise<Array>} 恢复的根目录信息数组
     */
    async function loadImportedRootsFromServer() {
        try {
            if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
                const roots = await WailsBridge.getImportedRoots();
                if (Array.isArray(roots) && roots.length > 0) {
                    let addedCount = 0;
                    for (const r of roots) {
                        const rootId = r.path || r.Path;
                        if (!importedRoots.some(existing => existing.rootId === rootId)) {
                            importedRoots.push({
                                rootId: rootId,
                                path: rootId,
                                name: r.name || r.Name || '',
                                handleName: r.handleName || r.HandleName || '',
                                displayName: r.displayName || r.DisplayName || r.name || r.Name || '',
                                folderType: r.folderType || r.FolderType || ''
                            });
                            addedCount++;
                        }
                    }
                    if (addedCount > 0) {
                        console.log(`[Gallery] 从 SQLite 恢复了 ${addedCount} 个导入文件夹信息`);
                    }
                    return roots;
                }
            }

            // fallback: 从 settings 加载
            let registeredRoots = [];
            try {
                registeredRoots = await Storage.getRegisteredRoots();
            } catch (err) {
                console.warn('[Gallery] 获取 registeredRoots 失败:', err.message);
            }
            const saved = await Storage.getSetting('importedRoots', []);
            if (Array.isArray(saved) && saved.length > 0) {
                const registeredSet = new Set((registeredRoots || []).map(r =>
                    r.replace(/\\/g, '/').toLowerCase()
                ));
                let addedCount = 0;
                for (const root of saved) {
                    const normalizedRootId = (root.rootId || '').replace(/\\/g, '/').toLowerCase();
                    const normalizedPath = (root.path || '').replace(/\\/g, '/').toLowerCase();
                    const isServerRegistered = registeredSet.has(normalizedRootId) || registeredSet.has(normalizedPath);
                    if (isServerRegistered) {
                        if (!importedRoots.some(r => r.rootId === root.rootId)) {
                            importedRoots.push({
                                rootId: root.rootId,
                                path: root.path || root.rootId,
                                name: root.name,
                                handleName: root.name,
                                displayName: root.displayName || root.name
                            });
                            addedCount++;
                        }
                    }
                }
                for (const goRoot of (registeredRoots || [])) {
                    const normGoRoot = goRoot.replace(/\\/g, '/');
                    if (!importedRoots.some(r => (r.rootId || '').replace(/\\/g, '/') === normGoRoot)) {
                        const name = goRoot.split(/[\\/]/).pop();
                        importedRoots.push({
                            rootId: goRoot, path: goRoot, name: name,
                            handleName: name, displayName: name
                        });
                        addedCount++;
                    }
                }
                if (addedCount > 0) {
                    console.log(`[Gallery] 从后端恢复了 ${addedCount} 个导入文件夹信息`);
                }
                return saved;
            }
        } catch (err) {
            console.warn('[Gallery] 从后端加载导入文件夹信息:', err.message);
        }
        return [];
    }

    // ==================== 搜索结果显示 ====================

    /**
     * 显示搜索结果（由 SearchModule 调用）
     * @param {Array} galleryImages - 已转换为 Gallery 格式的图片数组
     * @param {string} query - 搜索关键词
     * @param {number} total - 搜索结果总数
     * @param {boolean} append - 是否追加（加载更多）
     */
    async function displaySearchResults(galleryImages, query, total, append) {
        const isWails = typeof WailsBridge !== 'undefined' && WailsBridge.isWails();

        // 确保 cachedHttpBaseURL 已获取（搜索可能在浏览文件夹之前触发）
        await ensureBaseURLs();

        // 为搜索结果设置 URL
        for (const img of galleryImages) {
            if (isWails && cachedHttpBaseURL) {
                img.url = (cachedImageBaseURL || cachedHttpBaseURL) + '/image/' + img.id;
                img.thumbnailUrl = makeThumbURL(img.id, img.lastModified);
                img._loaded = true;
            } else if (img.url && img.thumbnailUrl) {
                img._loaded = true;
            }
        }

        if (!append) {
            // 首次搜索：清空旧结果
            searchResults = [];
            isSearchMode = true;
            searchQuery = query;
            searchTotalCount = total;
            isFilteringActive = true;
            currentFolderFilter = null;

            // 取消之前的渐进渲染
            cancelProgressiveRender();
            abortFolderSwitch();

            searchResults = galleryImages;
            filteredImages = searchResults;

            galleryScroll.scrollTop = 0;
            scrollTop = 0;
            renderedRange = { start: -1, end: -1 };

            sortImages();
            if (currentLayout === 'masonry') {
                renderMasonry(searchResults);
            } else if (currentLayout === 'pinterest') {
                renderPinterest(searchResults);
            } else if (currentLayout === 'list') {
                renderList(searchResults);
            } else {
                render();
            }
        } else {
            // 追加模式：增量渲染，只添加新卡片而不是重建整个 DOM
            // 不重新排序，保持已有图片位置不变，新图片追加到末尾
            searchResults = searchResults.concat(galleryImages);
            filteredImages = searchResults;

            // 只渲染新增的图片卡片，追加到现有网格
            appendImageCards(galleryImages);
            updateImageCount();
        }
    }

    /**
     * 增量追加图片卡片（搜索结果加载更多时使用）
     * 虚拟滚动模式下只需失效渲染范围后重新渲染
     * @param {Array} newImages - 要追加的新图片数组
     */
    function appendImageCards(newImages) {
        if (!newImages || newImages.length === 0) return;

        if (currentLayout === 'masonry') {
            // ★ 追加前重算整体布局，更新容器高度，确保滚动条范围正确
            const allDisplayImages = isFilteringActive ? filteredImages : images;
            computeMasonryLayout(allDisplayImages);
            galleryGrid.style.height = masonryTotalHeight + 'px';
            galleryGrid.style.minHeight = '';

            // 只为新增图片创建卡片（布局坐标已在 masonryLayout 末尾）
            const startIdx = allDisplayImages.length - newImages.length;
            const BATCH = getOptimalBatchSize() * 3; // 30~60 张/帧
            const newLayoutItems = masonryLayout.slice(startIdx);

            if (newLayoutItems.length <= BATCH) {
                const fragment = document.createDocumentFragment();
                for (const item of newLayoutItems) {
                    if (item.imgIndex < allDisplayImages.length) {
                        fragment.appendChild(createImageCard(allDisplayImages[item.imgIndex], item));
                    }
                }
                galleryGrid.appendChild(fragment);
            } else {
                let i = 0;
                function insertBatch() {
                    const fragment = document.createDocumentFragment();
                    const end = Math.min(i + BATCH, newLayoutItems.length);
                    for (; i < end; i++) {
                        const item = newLayoutItems[i];
                        if (item.imgIndex < allDisplayImages.length) {
                            fragment.appendChild(createImageCard(allDisplayImages[item.imgIndex], item));
                        }
                    }
                    galleryGrid.appendChild(fragment);
                    if (i < newLayoutItems.length) {
                        requestAnimationFrame(insertBatch);
                    }
                }
                requestAnimationFrame(insertBatch);
            }
        } else if (currentLayout === 'pinterest') {
            // Pinterest：重算整体布局，更新容器高度
            const allDisplayImages = isFilteringActive ? filteredImages : images;
            computePinterestLayout(allDisplayImages);
            galleryGrid.style.height = pinterestTotalHeight + 'px';
            galleryGrid.style.minHeight = '';

            const startIdx = allDisplayImages.length - newImages.length;
            const newLayoutItems = pinterestLayout.slice(startIdx);

            const fragment = document.createDocumentFragment();
            for (const item of newLayoutItems) {
                if (item.imgIndex < allDisplayImages.length) {
                    fragment.appendChild(createImageCard(allDisplayImages[item.imgIndex], { layout: 'pinterest', item: item }));
                }
            }
            galleryGrid.appendChild(fragment);
        } else if (currentLayout === 'list') {
            // 列表模式：两列，重算容器高度
            const allDisplayImages = isFilteringActive ? filteredImages : images;
            const gap = 8;
            const padding = 24;
            const containerW = Math.max(300, (galleryScroll ? galleryScroll.clientWidth : 800) - padding);
            const colW = Math.floor((containerW - gap) / 2);
            const rowStep = thumbnailSize + gap;
            const totalRows = Math.ceil(allDisplayImages.length / 2);
            listTotalHeight = totalRows * rowStep;
            galleryGrid.style.height = listTotalHeight + 'px';
            galleryGrid.style.minHeight = '';

            const fragment = document.createDocumentFragment();
            const startIdx = allDisplayImages.length - newImages.length;
            for (let k = startIdx; k < allDisplayImages.length; k++) {
                const row = Math.floor(k / 2);
                const col = k % 2;
                const x = col * (colW + gap);
                const y = row * rowStep;
                const layoutInfo = { layout: 'list', x: x, y: y, w: colW, h: thumbnailSize };
                fragment.appendChild(createImageCard(allDisplayImages[k], layoutInfo));
            }
            galleryGrid.appendChild(fragment);
        } else {
            // 网格模式：失效渲染范围后调用 renderGrid 全量重建虚拟滚动 DOM
            // 直接追加卡片会破坏虚拟滚动的 spacer 结构，导致滚动条失效
            renderedRange = { start: -1, end: -1 };
            _lastRenderScrollTop = scrollTop;
            const allDisplayImages = isFilteringActive ? filteredImages : images;
            renderGrid(allDisplayImages);
        }
    }

    /**
     * 清除搜索模式，恢复正常的文件夹浏览
     */
    function clearSearchResults() {
        isSearchMode = false;
        searchResults = [];
        searchTotalCount = 0;
        searchQuery = '';
        isFilteringActive = !!currentFolderFilter;
        applyCurrentFilter();
        sortImages();
        render();
    }

    // ==================== 公开 API ====================

    return {
        init,
        importFromFileList,
        importFromDirectoryPicker,
        loadImagesFromServer,         // ★ 从后端 API 加载图片列表
        mergeServerImages,            // ★ 将后端图片合并到全局 images 数组
        refreshRootFromServer,        // ★ 以"重新导入"方式刷新根目录
        refreshThumbGen,              // ★ 清缓存后刷新缩略图版本号
        restoreFromDirectoryHandles,
        saveImportedRootsToServer,    // ★ 跨浏览器持久化保存
        loadImportedRootsFromServer,  // ★ 跨浏览器持久化恢复
        reloadImportedRoot,
        removeImportedRoot,
        renameImportedRoot,
        getImportedRoots,
        addImportedRoot,              // ★ 手动添加 importedRoot 记录
        loadFromServer,
        loadAll,
        hasImages,
        getFolderTree,
        filterByFolder,
        filterByTag,
        filterByFavorites,
        clearFilters,
        render,
        refreshCurrentFilteredView,     // ★ 重新从 Storage 读取数据并刷新筛选视图
        refreshPromptCounts: async () => {
            if (!showPromptCount) return;
            promptCountMap = {};
            render();
        },
        getSelectedImages,
        clearSelection,
        scrollToImage,
        setActiveImage,
        batchAddTag,
        batchRemoveTag,
        batchToggleFavorite,
        batchRemoveFavorite,
        batchDelete,
        startBatchReverse,
        getImages: () => isFilteringActive ? filteredImages : images,
        triggerLoadMore: loadMoreFolderImages,
        hasMoreImages: () => folderLoadTotal > 0 ? folderLoadOffset < folderLoadTotal : false,
        getAllImages: () => images,
        getCurrentFolderFilter: () => currentFolderFilter,
        getCurrentTagFilter: () => currentTagFilter,
        isFilteringFavorites: () => currentFavoriteFilter,
        getCurrentFolder: () => {
            if (!currentFolderFilter) return null;
            const normalized = currentFolderFilter.replace(/\\/g, '/');
            for (const root of importedRoots) {
                const rootId = (root.rootId || '').replace(/\\/g, '/');
                if (normalized === rootId || normalized.startsWith(rootId + '/')) {
                    return {
                        path: currentFolderFilter,
                        name: root.name,
                        displayName: root.displayName || root.name
                    };
                }
            }
            return { path: currentFolderFilter, name: currentFolderFilter.split('/').pop(), displayName: currentFolderFilter.split('/').pop() };
        },
        // 增量刷新：返回当前浏览的文件夹路径和所属根目录
        getCurrentContext: () => {
            if (!currentFolderFilter) return null;
            const normalized = currentFolderFilter.replace(/\\/g, '/');
            for (const root of importedRoots) {
                const rootId = (root.rootId || root.path || '').replace(/\\/g, '/');
                if (normalized === rootId || normalized.startsWith(rootId + '/')) {
                    return { rootPath: root.rootId || root.path, folderPath: currentFolderFilter };
                }
            }
            return null;
        },
        getTagFilter: () => currentTagFilter,
        getFavoriteFilter: () => currentFavoriteFilter,
        // 增量应用刷新差异：移除删除的图片，追加新增的图片，然后重建可见区域
        applyFolderRefresh: (diff) => {
            // 构建临时 imageMap（从当前 images 数组），O(n) 仅执行一次
            imageMap.clear();
            for (const img of images) {
                imageMap.set(img.id, img);
            }

            // 移除已删除的图片 — O(removed.length)
            const removedSet = new Set(diff.removed);
            for (const id of diff.removed) {
                imageMap.delete(id);
            }

            // 追加新增的图片 — O(added.length)，转换为前端格式
            const isWails = typeof WailsBridge !== 'undefined' && WailsBridge.isWails();
            for (const safeImg of diff.added) {
                const imageURL = isWails ? ((cachedImageBaseURL || cachedHttpBaseURL) + '/image/' + safeImg.id) : safeImg.url;
                const thumbURL = isWails ? (makeThumbURL(safeImg.id, safeImg.lastModified)) : (safeImg.thumbUrl || safeImg.url);
                imageMap.set(safeImg.id, {
                    id: safeImg.id,
                    path: safeImg.path,
                    name: safeImg.name,
                    size: safeImg.size,
                    lastModified: safeImg.lastModified,
                    createdAt: safeImg.createdAt || 0,
                    folder: safeImg.folder,
                    rootPath: safeImg.rootPath,
                    rootId: safeImg.rootPath,
                    url: imageURL,
                    thumbnailUrl: thumbURL,
                    width: safeImg.width || 0,
                    height: safeImg.height || 0,
                    displayName: safeImg.rootPath ? safeImg.rootPath.split(/[\\/]/).pop() : '',
                    metadata: null,
                    file: null,
                    _loaded: true,
                    _fromServer: true,
                    isVideo: img.isVideo || false
                });
            }

            // 同步 images 数组
            images = [...imageMap.values()];

            // 同步 filteredImages（当前处于文件夹过滤模式）
            if (isFilteringActive && currentFolderFilter) {
                const normFilter = currentFolderFilter.replace(/\\/g, '/');
                filteredImages = images.filter(img => {
                    if (!img._fromServer) return false;
                    const imgFolder = (img.folder || '').replace(/\\/g, '/');
                    const imgRoot = (img.rootPath || '').replace(/\\/g, '/');
                    const fullPath = imgFolder ? imgRoot + '/' + imgFolder : imgRoot;
                    return fullPath === normFilter || fullPath.startsWith(normFilter + '/');
                });
            }

            // 更新缓存元数据
            if (currentFolderFilter) {
                const normalized = currentFolderFilter.replace(/\\/g, '/');
                const folderImages = images.filter(img => {
                    if (!img._fromServer) return false;
                    const imgFolder = (img.folder || '').replace(/\\/g, '/');
                    const imgRoot = (img.rootPath || '').replace(/\\/g, '/');
                    const fullPath = imgFolder ? imgRoot + '/' + imgFolder : imgRoot;
                    return fullPath === normalized || fullPath.startsWith(normalized + '/');
                });
                folderCacheMeta[normalized] = { total: folderImages.length };
            }

            // 重置虚拟滚动范围，触发仅可见区域的 DOM 重建
            // render() 内部会调用 updateImageCount()
            renderedRange = { start: -1, end: -1 };
            render();
        },
        resolveMetadataOnDemand,
        // 渐进式渲染相关
        progressiveRender,
        cancelProgressiveRender,
        isProgressivelyRendering,
        // 按需加载
        lazyLoadFolder,
        // 编辑模式
        isEditMode: () => isEditMode,
        setEditMode: (enabled) => {
            isEditMode = enabled;
            if (!enabled) {
                clearSelection();
            }
            if (onEditModeChange) {
                onEditModeChange(enabled);
            }
        },
        toggleEditMode: () => {
            const newMode = !isEditMode;
            isEditMode = newMode;
            if (!newMode) {
                clearSelection();
            }
            if (onEditModeChange) {
                onEditModeChange(newMode);
            }
            return newMode;
        },
        // 搜索模式
        isSearchMode: () => isSearchMode,
        getSearchQuery: () => searchQuery,
        getSearchTotal: () => searchTotalCount,
        displaySearchResults,
        clearSearchResults,
        // 全量渲染当前搜索结果（"显示全部结果"按钮调用）
        renderAllSearchResults: () => {
            if (!isSearchMode || searchResults.length === 0) return;
            cancelProgressiveRender();
            if (currentLayout === 'masonry') {
                renderMasonry(searchResults);
            } else if (currentLayout === 'pinterest') {
                renderPinterest(searchResults);
            } else if (currentLayout === 'list') {
                renderList(searchResults);
            } else {
                renderGridFull(searchResults);
            }
        }
    };
})();

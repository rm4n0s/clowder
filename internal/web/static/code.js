// The toolbar of the code blocks (SPEC §7.3, D54). No library and no build
// step (D13). Every block the server renders is
//
//   <pre class="code" data-lang="go"><code>…</code></pre>
//
// and this script puts a toolbar in front of it: the name of the language (when
// there is one) and a Copy button. The toolbar is a separate element before the
// <pre>, so that what Copy copies is the code and never the label.
//
// Blocks arrive after the page has loaded too (a live post or comment, a
// preview), and Datastar may put a post back in the page without the toolbar,
// so a MutationObserver gives every block that has no toolbar one.
(function () {
  'use strict';

  var COPIED_MS = 2000;

  // The toolbar of a block is the element in front of it with this class.
  function hasToolbar(pre) {
    var prev = pre.previousElementSibling;
    return !!prev && prev.classList.contains('code-toolbar');
  }

  // Selects the text of the code, for the browsers that cannot write to the
  // clipboard (a page over http has no navigator.clipboard).
  function selectCode(code) {
    var selection = window.getSelection();
    var range = document.createRange();
    range.selectNodeContents(code);
    selection.removeAllRanges();
    selection.addRange(range);
  }

  function copyCode(pre, button) {
    var code = pre.querySelector('code') || pre;
    var text = code.textContent;
    var done = function () {
      button.textContent = 'Copied';
      window.setTimeout(function () {
        button.textContent = 'Copy';
      }, COPIED_MS);
    };
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(done, function () {
        selectCode(code);
      });
    } else {
      selectCode(code);
    }
  }

  function addToolbar(pre) {
    if (hasToolbar(pre)) {
      return;
    }
    var bar = document.createElement('div');
    bar.className = 'code-toolbar';

    var lang = pre.getAttribute('data-lang');
    if (lang) {
      var label = document.createElement('span');
      label.className = 'code-lang';
      label.textContent = lang;
      bar.appendChild(label);
    }

    var button = document.createElement('button');
    button.type = 'button';
    button.className = 'code-copy';
    button.setAttribute('aria-label', 'Copy code');
    button.textContent = 'Copy';
    button.addEventListener('click', function () {
      copyCode(pre, button);
    });
    bar.appendChild(button);

    pre.parentNode.insertBefore(bar, pre);
  }

  function scan(root) {
    if (root.nodeType !== Node.ELEMENT_NODE && root.nodeType !== Node.DOCUMENT_NODE) {
      return;
    }
    if (root.matches && root.matches('pre.code')) {
      addToolbar(root);
    }
    var blocks = root.querySelectorAll('pre.code');
    for (var i = 0; i < blocks.length; i++) {
      addToolbar(blocks[i]);
    }
  }

  function start() {
    scan(document);
    new MutationObserver(function (records) {
      for (var i = 0; i < records.length; i++) {
        var added = records[i].addedNodes;
        for (var j = 0; j < added.length; j++) {
          scan(added[j]);
        }
        // A patch can take the toolbar away and keep the block.
        if (records[i].removedNodes.length > 0) {
          scan(records[i].target);
        }
      }
    }).observe(document.body, { childList: true, subtree: true });
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', start);
  } else {
    start();
  }
})();

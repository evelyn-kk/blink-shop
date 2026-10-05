import { cleanup } from '@testing-library/react';
import { afterEach, beforeEach } from 'vitest';
import { resetNotices } from '../lib/notice';

// jsdom 没有实现 <dialog> 的模态方法，这里按规范的可见行为补上（open 属性）。
if (typeof HTMLDialogElement !== 'undefined' && !HTMLDialogElement.prototype.showModal) {
  HTMLDialogElement.prototype.showModal = function showModal(this: HTMLDialogElement) {
    this.setAttribute('open', '');
  };
  HTMLDialogElement.prototype.close = function close(this: HTMLDialogElement) {
    this.removeAttribute('open');
    this.dispatchEvent(new Event('close'));
  };
}

beforeEach(() => {
  window.localStorage.clear();
  window.location.hash = '';
});

afterEach(() => {
  cleanup();
  resetNotices();
});

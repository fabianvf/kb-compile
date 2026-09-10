import { render } from './client.js';

it('renders', () => {
  expect(render(1)).toBe('1 pts');
});

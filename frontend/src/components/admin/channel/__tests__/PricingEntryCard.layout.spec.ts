import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

vi.mock('@/api/admin/channels', () => ({
  default: {
    getModelDefaultPricing: vi.fn(),
  },
}))

import PricingEntryCard from '../PricingEntryCard.vue'
import type { PricingFormEntry } from '../types'

function baseEntry(): PricingFormEntry {
  return {
    models: [],
    billing_mode: 'token',
    input_price: null,
    output_price: null,
    cache_write_price: null,
    cache_read_price: null,
    image_output_price: null,
    per_request_price: null,
    intervals: [
      {
        min_tokens: 0,
        max_tokens: null,
        tier_label: '',
        input_price: null,
        output_price: null,
        cache_write_price: null,
        cache_read_price: null,
        per_request_price: null,
        sort_order: 0,
      },
    ],
  }
}

describe('PricingEntryCard 布局', () => {
  it('展开区域为输入框和下拉框的 focus ring 保留安全区', () => {
    const wrapper = mount(PricingEntryCard, {
      props: {
        entry: baseEntry(),
        platform: 'openai',
      },
      global: {
        stubs: {
          Icon: true,
        },
      },
    })

    const inner = wrapper.get('.collapsible-inner')
    expect(inner.classes()).toContain('px-0.5')
    expect(inner.classes()).toContain('pb-0.5')
  })
})

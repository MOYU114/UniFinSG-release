<script setup>
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import * as echarts from 'echarts/core'
import { PieChart } from 'echarts/charts'
import { TooltipComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import { CATEGORY_MAP } from '../../constants/finance'
import { formatMoney } from '../../utils/format'

echarts.use([PieChart, TooltipComponent, CanvasRenderer])

const props = defineProps({ categories: { type: Array, default: () => [] } })
const chartElement = ref(null)
let chart
let resizeObserver

function render() {
  if (!chartElement.value) return
  if (!chart) chart = echarts.init(chartElement.value)
  const data = props.categories
    .filter((item) => item.spent_cents > 0)
    .map((item) => ({
      name: CATEGORY_MAP[item.category]?.label || item.category,
      value: item.spent_cents,
      itemStyle: { color: CATEGORY_MAP[item.category]?.color },
    }))

  chart.setOption({
    animationDuration: 550,
    tooltip: {
      trigger: 'item',
      formatter: (params) => `${params.marker}${params.name}<br/><strong>${formatMoney(params.value)}</strong> · ${params.percent}%`,
      borderWidth: 0,
      padding: [10, 12],
      textStyle: { color: '#25352f', fontFamily: 'Inter, sans-serif' },
    },
    series: [{
      type: 'pie',
      radius: ['62%', '87%'],
      center: ['50%', '50%'],
      avoidLabelOverlap: true,
      label: { show: false },
      emphasis: { scaleSize: 5 },
      itemStyle: { borderColor: '#fff', borderWidth: 4, borderRadius: 8 },
      data: data.length ? data : [{ name: 'No spending', value: 1, itemStyle: { color: '#e9ece7' } }],
    }],
  })
}

watch(() => props.categories, render, { deep: true })
onMounted(() => {
  render()
  resizeObserver = new ResizeObserver(() => chart?.resize())
  resizeObserver.observe(chartElement.value)
})
onBeforeUnmount(() => {
  resizeObserver?.disconnect()
  chart?.dispose()
})
</script>

<template><div ref="chartElement" class="spending-donut" role="img" aria-label="Spending by category chart"></div></template>
